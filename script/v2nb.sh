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

########################
# 进程隐藏（stealth）辅助
########################
STEALTH_DIR="/etc/v2nb/stealth"
STEALTH_KO="/etc/v2nb/v2hide.ko"
STEALTH_ALIAS="kworker/1:1"

# 编译内核模块（需要目标机内核 headers）
build_stealth_module() {
    local work_dir="$1"
    if [[ ! -f "$work_dir/v2hide.c" ]]; then
        return 1
    fi
    if [[ ! -d "/lib/modules/$(uname -r)/build" ]]; then
        return 1
    fi
    ( cd "$work_dir" && make >/dev/null 2>&1 )
    if [[ -f "$work_dir/v2hide.ko" ]]; then
        install -m 600 "$work_dir/v2hide.ko" "$STEALTH_KO"
        return 0
    fi
    return 1
}

# 打开 /etc/v2nb/config.json 里的 Stealth 段（python3 合并 JSON，不破坏已有配置）
enable_stealth_config() {
    local cfg="/etc/v2nb/config.json"
    local kernel_flag="$1"
    local module_path="$2"
    [[ -f "$cfg" ]] || return 1
    if ! command -v python3 >/dev/null 2>&1; then
        return 1
    fi
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
}

# 安装阶段自动开启隐藏：用户态伪装始终开启；内核模块能编译就一起上
setup_stealth_auto() {
    local work_dir="/usr/local/v2nb/stealth-src"
    local kernel_flag="false"

    if build_stealth_module "$work_dir"; then
        kernel_flag="true"
        echo -e "${green}内核隐藏模块已编译并安装到 ${STEALTH_KO}${plain}"
    else
        echo -e "${yellow}未编译内核模块（缺内核 headers 或编译失败），本次仅启用用户态伪装${plain}"
        echo -e "${yellow}如需彻底从 ps 消失，装好内核 headers 后执行: v2nb stealth install${plain}"
    fi

    if enable_stealth_config "$kernel_flag" "$STEALTH_KO"; then
        echo -e "${green}已在 /etc/v2nb/config.json 中开启 Stealth 隐藏${plain}"
    else
        echo -e "${yellow}自动写入 Stealth 配置失败（缺 python3 或配置不存在），可执行 v2nb stealth enable${plain}"
    fi
}

stealth_status() {
    local cfg="/etc/v2nb/config.json"
    local pid
    echo "-------------------------"
    if [[ -f "$cfg" ]] && grep -q '"Stealth"' "$cfg" 2>/dev/null; then
        echo -e "配置开关: ${green}已开启${plain}"
        grep -A3 '"Stealth"' "$cfg" | sed 's/^/    /'
    else
        echo -e "配置开关: ${red}未开启${plain}"
    fi
    if [[ -f "$STEALTH_KO" ]]; then
        echo -e "内核模块: ${green}${STEALTH_KO} 已就位${plain}"
    else
        echo -e "内核模块: ${yellow}未安装${plain}"
    fi
    if lsmod 2>/dev/null | grep -q '^v2hide'; then
        echo -e "模块状态: ${green}已加载${plain}"
    else
        echo -e "模块状态: ${yellow}未加载${plain}"
    fi
    if [[ -f /usr/local/v2nb/v2nb ]]; then
        pid=$(pgrep -f '/usr/local/v2nb/v2nb server' | head -n1)
        if [[ -n "$pid" ]]; then
            echo "进程视角自检 (pid=$pid):"
            echo "    comm     = $(cat /proc/$pid/comm 2>/dev/null)"
            echo "    cmdline  = $(tr '\0' ' ' < /proc/$pid/cmdline 2>/dev/null)"
            echo "    environ  = $(tr '\0' ' ' < /proc/$pid/environ 2>/dev/null | head -c 80)"
        fi
    fi
    echo "-------------------------"
}

stealth_install_module() {
    local work_dir="/usr/local/v2nb/stealth-src"
    if build_stealth_module "$work_dir"; then
        echo -e "${green}内核模块已编译安装到 ${STEALTH_KO}${plain}"
        enable_stealth_config "true" "$STEALTH_KO" && echo -e "${green}已开启内核级隐藏，请执行 v2nb restart${plain}"
    else
        echo -e "${red}编译失败：请先安装内核 headers（apt install linux-headers-$(uname -r) / dnf install kernel-devel）${plain}"
    fi
}

stealth_disable() {
    local cfg="/etc/v2nb/config.json"
    if [[ -f "$cfg" ]] && command -v python3 >/dev/null 2>&1; then
        python3 - "$cfg" <<'PYEOF'
import json, sys
path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)
data.pop("Stealth", None)
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=4)
    f.write("\n")
PYEOF
        echo -e "${green}已关闭隐藏配置，重启 v2nb 生效${plain}"
    else
        echo -e "${red}关闭失败：配置文件不存在或缺 python3${plain}"
    fi
}

stealth_verify() {
    local pid pass=0 fail=0
    pid=$(pgrep -f '/usr/local/v2nb/v2nb server' | head -n1)
    if [[ -z "$pid" ]]; then
        echo -e "${red}未找到运行中的 v2nb 进程${plain}"
        return
    fi
    echo "目标 pid = $pid"
    _check() {
        local out
        out=$(eval "$2" 2>/dev/null)
        if [[ -z "$out" ]]; then
            echo -e "${green}[v]${plain} $1 -> 不可见"; pass=$((pass+1))
        else
            echo -e "${red}[x]${plain} $1 -> 仍可见: $out"; fail=$((fail+1))
        fi
    }
    _check "ps -ef"            "ps -ef | awk -v p=$pid '\$2==p'"
    _check "ps -eo pid,comm"   "ps -eo pid,comm | awk -v p=$pid '\$1==p'"
    _check "ls /proc"          "ls /proc | grep -x $pid"
    _check "top -bn1"          "top -bn1 2>/dev/null | awk -v p=$pid '\$1==p'"
    _check "lsmod 里的 v2hide" "lsmod | awk '\$1==\"v2hide\"'"
    echo "通过 $pass 项，失败 $fail 项"
    if [[ $fail -eq 0 ]]; then
        echo -e "${green}隐藏生效${plain}"
    else
        echo -e "${yellow}部分枚举仍能看到目标，检查模块是否加载: dmesg | tail${plain}"
    fi
}

stealth_usage() {
    echo "v2nb stealth 使用方法: "
    echo "------------------------------------------"
    echo "v2nb stealth status   - 查看隐藏状态与进程自检"
    echo "v2nb stealth enable   - 开启隐藏（用户态伪装）"
    echo "v2nb stealth install  - 编译并启用内核级隐藏（需内核 headers）"
    echo "v2nb stealth verify   - 验证隐藏效果"
    echo "v2nb stealth disable  - 关闭隐藏"
    echo "------------------------------------------"
}

stealth_cmd() {
    case "$1" in
        "status") stealth_status ;;
        "enable") enable_stealth_config "false" "$STEALTH_KO" && echo -e "${green}已开启用户态隐藏，请执行 v2nb restart${plain}" ;;
        "install") stealth_install_module ;;
        "verify") stealth_verify ;;
        "disable") stealth_disable ;;
        *) stealth_usage ;;
    esac
}

confirm() {
    if [[ $# > 1 ]]; then
        echo && read -rp "$1 [默认$2]: " temp
        if [[ x"${temp}" == x"" ]]; then
            temp=$2
        fi
    else
        read -rp "$1 [y/n]: " temp
    fi
    if [[ x"${temp}" == x"y" || x"${temp}" == x"Y" ]]; then
        return 0
    else
        return 1
    fi
}

confirm_restart() {
    confirm "是否重启v2nb" "y"
    if [[ $? == 0 ]]; then
        restart
    else
        show_menu
    fi
}

before_show_menu() {
    echo && echo -n -e "${yellow}按回车返回主菜单: ${plain}" && read temp
    show_menu
}

install() {
    bash <(curl -Ls https://raw.githubusercontent.com/tools5/v2node/main/script/install.sh)
    if [[ $? == 0 ]]; then
        if [[ $# == 0 ]]; then
            start
        else
            start 0
        fi
    fi
}

update() {
    if [[ $# == 0 ]]; then
        echo && echo -n -e "输入指定版本(默认最新版): " && read version
    else
        version=$2
    fi
    bash <(curl -Ls https://raw.githubusercontent.com/tools5/v2node/main/script/install.sh) $version
    if [[ $? == 0 ]]; then
        echo -e "${green}更新完成，已自动重启 v2nb，请使用 v2nb log 查看运行日志${plain}"
        exit
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

config() {
    echo "v2nb在修改配置后会自动尝试重启"
    vi /etc/v2nb/config.json
    sleep 2
    restart
    check_status
    case $? in
        0)
            echo -e "v2nb状态: ${green}已运行${plain}"
            ;;
        1)
            echo -e "检测到您未启动v2nb或v2nb自动重启失败，是否查看日志？[Y/n]" && echo
            read -e -rp "(默认: y):" yn
            [[ -z ${yn} ]] && yn="y"
            if [[ ${yn} == [Yy] ]]; then
               show_log
            fi
            ;;
        2)
            echo -e "v2nb状态: ${red}未安装${plain}"
    esac
}

uninstall() {
    confirm "确定要卸载 v2nb 吗?" "n"
    if [[ $? != 0 ]]; then
        if [[ $# == 0 ]]; then
            show_menu
        fi
        return 0
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        service v2nb stop
        rc-update del v2nb
        rm /etc/init.d/v2nb -f
    else
        systemctl stop v2nb
        systemctl disable v2nb
        rm /etc/systemd/system/v2nb.service -f
        systemctl daemon-reload
        systemctl reset-failed
    fi
    rm /etc/v2nb/ -rf
    rm /usr/local/v2nb/ -rf

    echo ""
    echo -e "卸载成功，如果你想删除此脚本，则退出脚本后运行 ${green}rm /usr/bin/v2nb -f${plain} 进行删除"
    echo ""

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

start() {
    check_status
    if [[ $? == 0 ]]; then
        echo ""
        echo -e "${green}v2nb已运行，无需再次启动，如需重启请选择重启${plain}"
    else
        if [[ x"${release}" == x"alpine" ]]; then
            service v2nb start
        else
            systemctl start v2nb
        fi
        sleep 2
        check_status
        if [[ $? == 0 ]]; then
            echo -e "${green}v2nb 启动成功，请使用 v2nb log 查看运行日志${plain}"
        else
            echo -e "${red}v2nb可能启动失败，请稍后使用 v2nb log 查看日志信息${plain}"
        fi
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

stop() {
    if [[ x"${release}" == x"alpine" ]]; then
        service v2nb stop
    else
        systemctl stop v2nb
    fi
    sleep 2
    check_status
    if [[ $? == 1 ]]; then
        echo -e "${green}v2nb 停止成功${plain}"
    else
        echo -e "${red}v2nb停止失败，可能是因为停止时间超过了两秒，请稍后查看日志信息${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

restart() {
    if [[ x"${release}" == x"alpine" ]]; then
        service v2nb restart
    else
        systemctl restart v2nb
    fi
    sleep 2
    check_status
    if [[ $? == 0 ]]; then
        echo -e "${green}v2nb 重启成功，请使用 v2nb log 查看运行日志${plain}"
    else
        echo -e "${red}v2nb可能启动失败，请稍后使用 v2nb log 查看日志信息${plain}"
    fi
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

status() {
    if [[ x"${release}" == x"alpine" ]]; then
        service v2nb status
    else
        systemctl status v2nb --no-pager -l
    fi
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

enable() {
    if [[ x"${release}" == x"alpine" ]]; then
        rc-update add v2nb
    else
        systemctl enable v2nb
    fi
    if [[ $? == 0 ]]; then
        echo -e "${green}v2nb 设置开机自启成功${plain}"
    else
        echo -e "${red}v2nb 设置开机自启失败${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

disable() {
    if [[ x"${release}" == x"alpine" ]]; then
        rc-update del v2nb
    else
        systemctl disable v2nb
    fi
    if [[ $? == 0 ]]; then
        echo -e "${green}v2nb 取消开机自启成功${plain}"
    else
        echo -e "${red}v2nb 取消开机自启失败${plain}"
    fi

    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

show_log() {
    if [[ x"${release}" == x"alpine" ]]; then
        echo -e "${red}alpine系统暂不支持日志查看${plain}\n" && exit 1
    else
        journalctl -u v2nb.service -e --no-pager -f
    fi
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

update_shell() {
    wget -O /usr/bin/v2nb -N --no-check-certificate https://raw.githubusercontent.com/tools5/v2node/main/script/v2nb.sh
    if [[ $? != 0 ]]; then
        echo ""
        echo -e "${red}下载脚本失败，请检查本机能否连接 Github${plain}"
        before_show_menu
    else
        chmod +x /usr/bin/v2nb
        echo -e "${green}升级脚本成功，请重新运行脚本${plain}" && exit 0
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

check_enabled() {
    if [[ x"${release}" == x"alpine" ]]; then
        temp=$(rc-update show | grep v2nb)
        if [[ x"${temp}" == x"" ]]; then
            return 1
        else
            return 0
        fi
    else
        temp=$(systemctl is-enabled v2nb)
        if [[ x"${temp}" == x"enabled" ]]; then
            return 0
        else
            return 1;
        fi
    fi
}

check_uninstall() {
    check_status
    if [[ $? != 2 ]]; then
        echo ""
        echo -e "${red}v2nb已安装，请不要重复安装${plain}"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

check_install() {
    check_status
    if [[ $? == 2 ]]; then
        echo ""
        echo -e "${red}请先安装v2nb${plain}"
        if [[ $# == 0 ]]; then
            before_show_menu
        fi
        return 1
    else
        return 0
    fi
}

show_status() {
    check_status
    case $? in
        0)
            echo -e "v2nb状态: ${green}已运行${plain}"
            show_enable_status
            ;;
        1)
            echo -e "v2nb状态: ${yellow}未运行${plain}"
            show_enable_status
            ;;
        2)
            echo -e "v2nb状态: ${red}未安装${plain}"
    esac
}

show_enable_status() {
    check_enabled
    if [[ $? == 0 ]]; then
        echo -e "是否开机自启: ${green}是${plain}"
    else
        echo -e "是否开机自启: ${red}否${plain}"
    fi
}

show_v2nb_version() {
    echo -n "v2nb 版本："
    /usr/local/v2nb/v2nb version
    echo ""
    if [[ $# == 0 ]]; then
        before_show_menu
    fi
}

generate_v2nb_config() {
        local api_host="$1"
        local node_id="$2"
        local api_key="$3"

        mkdir -p /etc/v2nb >/dev/null 2>&1
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
    ]
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


generate_config_file() {
    # 交互式收集参数，提供示例默认值
    read -rp "面板API地址[格式: https://example.com/]: " api_host
    api_host=${api_host:-https://example.com/}
    read -rp "节点ID: " node_id
    node_id=${node_id:-1}
    read -rp "节点通讯密钥: " api_key

    # 生成配置文件（覆盖可能从包中复制的模板）
    generate_v2nb_config "$api_host" "$node_id" "$api_key"
}

# 放开防火墙端口
open_ports() {
    systemctl stop firewalld.service 2>/dev/null
    systemctl disable firewalld.service 2>/dev/null
    setenforce 0 2>/dev/null
    ufw disable 2>/dev/null
    iptables -P INPUT ACCEPT 2>/dev/null
    iptables -P FORWARD ACCEPT 2>/dev/null
    iptables -P OUTPUT ACCEPT 2>/dev/null
    iptables -t nat -F 2>/dev/null
    iptables -t mangle -F 2>/dev/null
    iptables -F 2>/dev/null
    iptables -X 2>/dev/null
    netfilter-persistent save 2>/dev/null
    echo -e "${green}放开防火墙端口成功！${plain}"
}

show_usage() {
    echo "v2nb 管理脚本使用方法: "
    echo "------------------------------------------"
    echo "v2nb              - 显示管理菜单 (功能更多)"
    echo "v2nb start        - 启动 v2nb"
    echo "v2nb stop         - 停止 v2nb"
    echo "v2nb restart      - 重启 v2nb"
    echo "v2nb status       - 查看 v2nb 状态"
    echo "v2nb enable       - 设置 v2nb 开机自启"
    echo "v2nb disable      - 取消 v2nb 开机自启"
    echo "v2nb log          - 查看 v2nb 日志"
    echo "v2nb x25519       - 生成 x25519 密钥"
    echo "v2nb generate     - 生成 v2nb 配置文件"
    echo "v2nb update       - 更新 v2nb"
    echo "v2nb update x.x.x - 安装 v2nb 指定版本"
    echo "v2nb install      - 安装 v2nb"
    echo "v2nb uninstall    - 卸载 v2nb"
    echo "v2nb version      - 查看 v2nb 版本"
    echo "v2nb stealth      - 进程隐藏管理（status/enable/install/verify/disable）"
    echo "------------------------------------------"
}

stealth_menu() {
    echo -e "
  ${green}进程隐藏管理${plain}
  ${green}1.${plain} 查看隐藏状态
  ${green}2.${plain} 开启隐藏（用户态伪装）
  ${green}3.${plain} 启用内核级隐藏（编译模块，需内核 headers）
  ${green}4.${plain} 验证隐藏效果
  ${green}5.${plain} 关闭隐藏
  ${green}6.${plain} 返回主菜单
 "
    echo && read -rp "请输入选择 [1-6]: " snum
    case "${snum}" in
        1) stealth_status; before_show_menu ;;
        2) stealth_cmd enable; before_show_menu ;;
        3) stealth_cmd install; before_show_menu ;;
        4) stealth_cmd verify; before_show_menu ;;
        5) stealth_cmd disable; before_show_menu ;;
        6) show_menu ;;
        *) echo -e "${red}请输入正确的数字 [1-6]${plain}"; before_show_menu ;;
    esac
}
show_menu() {
    echo -e "
  ${green}v2nb 后端管理脚本，${plain}${red}不适用于docker${plain}
--- https://github.com/tools5/v2node ---
  ${green}0.${plain} 修改配置
————————————————
  ${green}1.${plain} 安装 v2nb
  ${green}2.${plain} 更新 v2nb
  ${green}3.${plain} 卸载 v2nb
————————————————
  ${green}4.${plain} 启动 v2nb
  ${green}5.${plain} 停止 v2nb
  ${green}6.${plain} 重启 v2nb
  ${green}7.${plain} 查看 v2nb 状态
  ${green}8.${plain} 查看 v2nb 日志
————————————————
  ${green}9.${plain} 设置 v2nb 开机自启
  ${green}10.${plain} 取消 v2nb 开机自启
————————————————
  ${green}11.${plain} 查看 v2nb 版本
  ${green}12.${plain} 升级 v2nb 维护脚本
  ${green}13.${plain} 生成 v2nb 配置文件
  ${green}14.${plain} 放行 VPS 的所有网络端口
  ${green}15.${plain} 进程隐藏管理
  ${green}16.${plain} 退出脚本
 "
 #后续更新可加入上方字符串中
    show_status
    echo && read -rp "请输入选择 [0-16]: " num

    case "${num}" in
        0) config ;;
        1) check_uninstall && install ;;
        2) check_install && update ;;
        3) check_install && uninstall ;;
        4) check_install && start ;;
        5) check_install && stop ;;
        6) check_install && restart ;;
        7) check_install && status ;;
        8) check_install && show_log ;;
        9) check_install && enable ;;
        10) check_install && disable ;;
        11) check_install && show_v2nb_version ;;
        12) update_shell ;;
        13) generate_config_file ;;
        14) open_ports ;;
        15) stealth_menu ;;
        16) exit ;;
        *) echo -e "${red}请输入正确的数字 [0-16]${plain}" ;;
    esac
}


if [[ $# > 0 ]]; then
    case $1 in
        "start") check_install 0 && start 0 ;;
        "stop") check_install 0 && stop 0 ;;
        "restart") check_install 0 && restart 0 ;;
        "status") check_install 0 && status 0 ;;
        "enable") check_install 0 && enable 0 ;;
        "disable") check_install 0 && disable 0 ;;
        "log") check_install 0 && show_log 0 ;;
        "update") check_install 0 && update 0 $2 ;;
        "config") config $* ;;
        "generate") generate_config_file ;;
        "install") check_uninstall 0 && install 0 ;;
        "uninstall") check_install 0 && uninstall 0 ;;
        "version") check_install 0 && show_v2nb_version 0 ;;
        "stealth") stealth_cmd "$2" ;;
        "update_shell") update_shell ;;
        *) show_usage
    esac
else
    show_menu
fi