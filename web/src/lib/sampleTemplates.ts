import type { TemplateKind } from '../lib/api'

/** 三种类型的示例模板（契约第 26 节占位符），供模板编辑器一键填充。 */
export const SAMPLE_TEMPLATES: Record<TemplateKind, { name: string; content: string }> = {
  binary: {
    name: '二进制发布示例',
    content: `# 准备新版本目录（幂等可重跑）
set -e
RELEASE_DIR="{{RELEASE_DIR}}"
mkdir -p "$RELEASE_DIR"
cd "$RELEASE_DIR"

# 下载制品并校验
curl -fsSL "{{ARTIFACT_URL}}" -o app.tar.gz
echo "{{ARTIFACT_SHA256}}  app.tar.gz" | sha256sum -c -

# 解包并授权
tar -xzf app.tar.gz
chmod +x app

# 原子切换 current 符号链接
ln -sfn "$RELEASE_DIR" "{{ROOT}}/current"

# 重启服务（按实际进程管理方式调整）
systemctl restart {{APP_SLUG}} || true
echo "release {{VERSION}} ({{RELEASE_ID}}) deployed to {{ROOT}}"
`,
  },
  systemd: {
    name: 'systemd 服务示例',
    content: `# 写入 systemd unit 并重启服务（幂等）
set -e
RELEASE_DIR="{{RELEASE_DIR}}"
mkdir -p "$RELEASE_DIR"
curl -fsSL "{{ARTIFACT_URL}}" -o "$RELEASE_DIR/app"
echo "{{ARTIFACT_SHA256}}  $RELEASE_DIR/app" | sha256sum -c -
chmod +x "$RELEASE_DIR/app"
ln -sfn "$RELEASE_DIR" "{{ROOT}}/current"

cat > /etc/systemd/system/{{APP_SLUG}}.service <<'EOF'
[Unit]
Description={{APP_SLUG}} (managed by LeadStack)
After=network.target

[Service]
WorkingDirectory={{ROOT}}/current
ExecStart={{ROOT}}/current/app
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable {{APP_SLUG}}
systemctl restart {{APP_SLUG}}
echo "systemd unit {{APP_SLUG}}.service updated to {{VERSION}}"
`,
  },
  compose: {
    name: 'Docker Compose 示例',
    content: `# 用 Compose 部署镜像（幂等）
set -e
RELEASE_DIR="{{RELEASE_DIR}}"
mkdir -p "$RELEASE_DIR"
cd "$RELEASE_DIR"

cat > docker-compose.yml <<'EOF'
services:
  app:
    image: {{ARTIFACT_URL}}
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      APP_VERSION: "{{VERSION}}"
      RELEASE_ID: "{{RELEASE_ID}}"
EOF

docker compose pull
docker compose up -d --remove-orphans
ln -sfn "$RELEASE_DIR" "{{ROOT}}/current"
echo "compose release {{VERSION}} up (prev={{PREV_RELEASE_ID}})"
`,
  },
}

export const TEMPLATE_KIND_LABELS: Record<TemplateKind, string> = {
  compose: 'compose（Docker Compose 编排）',
  systemd: 'systemd（服务单元）',
  binary: 'binary（二进制 + 符号链接切换）',
}
