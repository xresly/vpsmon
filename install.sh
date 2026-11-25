#!/bin/bash
# vpsmon 一键安装 / 升级脚本（仿 3x-ui 的安装体验）
#
#   bash <(curl -Ls https://raw.githubusercontent.com/xresly/vpsmon/main/install.sh)
#   bash <(curl -Ls https://raw.githubusercontent.com/xresly/vpsmon/main/install.sh) v1.0.0
#
# 升级：已有 /etc/vpsmon/config.json 时只替换二进制并重启服务，配置保留。
# 可用环境变量：VPSMON_DL_BASE 覆盖下载前缀（镜像/测试用）。

set -e

REPO="xresly/vpsmon"
BIN_PATH="/usr/local/bin/vpsmon"
CONF_DIR="/etc/vpsmon"
CONF_FILE="${CONF_DIR}/config.json"
STATE_DIR="/var/lib/vpsmon"
UNIT_FILE="/etc/systemd/system/vpsmon.service"
DL_BASE="${VPSMON_DL_BASE:-https://github.com/${REPO}/releases/download}"

red() { echo -e "\033[31m$*\033[0m"; }
green() { echo -e "\033[32m$*\033[0m"; }
yellow() { echo -e "\033[33m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
    red "请用 root 运行此脚本"
    exit 1
fi

# 架构
case "$(uname -m)" in
    x86_64 | amd64) ARCH="amd64" ;;
    aarch64 | arm64) ARCH="arm64" ;;
    *) red "不支持的架构: $(uname -m)"; exit 1 ;;
esac

# 系统（只做提示，二进制是静态编译的，真正的依赖只有 systemd）
if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    case "${ID:-}" in
        debian | ubuntu) ;;
        *) yellow "未在 ${PRETTY_NAME:-未知系统} 上测试过，继续安装（需要 systemd）" ;;
    esac
fi
if ! command -v systemctl >/dev/null 2>&1; then
    red "未找到 systemctl，本脚本需要 systemd"
    exit 1
fi

# 版本：参数 > 环境变量 > GitHub 最新 Release > 默认 v1.0.0
VERSION="${1:-${VPSMON_VERSION:-}}"
if [[ -z "${VERSION}" ]]; then
    VERSION="$(curl -Ls "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/' || true)"
fi
if [[ -z "${VERSION}" ]]; then
    VERSION="v1.0.0"
    yellow "无法获取最新版本，使用默认 ${VERSION}"
fi
green "安装版本: ${VERSION} (${ARCH})"

# 下载并安装二进制
TMP_BIN="$(mktemp)"
trap 'rm -f "${TMP_BIN}"' EXIT
URL="${DL_BASE}/${VERSION}/vpsmon-linux-${ARCH}"
echo "下载 ${URL}"
if ! curl -fL --retry 3 -o "${TMP_BIN}" "${URL}"; then
    red "下载失败，请检查网络或版本号"
    exit 1
fi
chmod +x "${TMP_BIN}"
mkdir -p "${CONF_DIR}" "${STATE_DIR}"
install -m 0755 "${TMP_BIN}" "${BIN_PATH}"

# systemd 服务
cat > "${UNIT_FILE}" <<'EOF'
[Unit]
Description=vpsmon - VPS monitor panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/vpsmon run -config /etc/vpsmon/config.json
WorkingDirectory=/var/lib/vpsmon
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable vpsmon >/dev/null 2>&1 || true

ask() {
    # ask "提示" "默认值" -> 输出最终值（优先从 /dev/tty 读，兼容 curl | bash）
    local prompt="$1" def="$2" val=""
    if [[ -r /dev/tty ]]; then
        read -r -p "${prompt} [默认 ${def}]: " val < /dev/tty || val=""
    fi
    echo "${val:-$def}"
}

if [[ -f "${CONF_FILE}" ]]; then
    # ---- 升级模式 ----
    systemctl restart vpsmon
    green "=== vpsmon 已升级到 ${VERSION}，配置保持不变 ==="
    echo "查看面板信息: vpsmon menu（选 8）"
else
    # ---- 全新安装：交互配置 WebUI ----
    echo
    echo "配置 WebUI（回车用默认值）："
    DEF_PORT="$(shuf -i 20000-50000 -n 1 2>/dev/null || echo 2053)"
    PANEL_PORT="$(ask 'WebUI 端口' "${DEF_PORT}")"
    PANEL_USER="$(ask 'WebUI 用户名' "admin")"
    DEF_PASS="$(tr -dc 'a-zA-Z0-9' < /dev/urandom | head -c 12)"
    PANEL_PASS="$(ask 'WebUI 密码' "${DEF_PASS}")"

    "${BIN_PATH}" init -config "${CONF_FILE}" -port "${PANEL_PORT}" \
        -username "${PANEL_USER}" -password "${PANEL_PASS}" >/dev/null

    systemctl restart vpsmon

    # 公网 IP（拿不到就用本机 IP 兜底）
    PUB_IP="$(curl -s --max-time 5 https://api.ipify.org || curl -s --max-time 5 https://ifconfig.me || hostname -I | awk '{print $1}')"
    echo
    green "================ vpsmon 安装完成 ================"
    echo "面板地址: http://${PUB_IP}:${PANEL_PORT}"
    echo "用户名:   ${PANEL_USER}"
    echo "密码:     ${PANEL_PASS}"
    echo "--------------------------------------------------"
    echo "登录后在 WebUI 里开启「服务端」和/或「客户端」角色。"
    echo "管理命令: vpsmon menu"
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        yellow "检测到 ufw 已启用，别忘了放行面板端口: ufw allow ${PANEL_PORT}/tcp"
    fi
    green "=================================================="
fi
