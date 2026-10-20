// Package deploy 实现 LeadStack 部署 的部署编排核心：
// 模板渲染（Render）、执行计划编排（Build）、回滚计划（Rollback）与执行器（Executor）。
//
// 契约见 docs/ARCHITECTURE.md 第 23/24/25/26 节（签名与占位符已冻结）。
// 本包**不得** import internal/store / internal/api —— 依赖一律接口注入，保证可独立单测。
//
// # 目标机目录布局
//
// BuildParams.Root 是**部署根**（控制端经 LEADSTACK_DEPLOY_ROOT 下发），
// 应用根 = <Root>/<AppSlug>，并写入 Plan.Root；每个 release 一个不可变目录：
//
//	<Root>/<AppSlug>/                                    <- Plan.Root
//	├── current -> releases/<releaseID>                  <- 真符号链接，原子切换
//	├── releases/<releaseID>/                            <- 本次发布的不可变目录
//	│   ├── .artifact                                    <- 下载的制品（校验通过后解包/落位）
//	│   ├── docker-compose.yml / <slug>.service / leadstack-start.sh（按模板类型）
//	└── .leadstack/releases/<releaseID>.json             <- 发布元数据快照
//
// 部署顺序固定为「准备目录 → 下载 → 校验 sha256 → 解包/落位 →（写文件）→ 切 current → 重启 → 清理」，
// **校验必须发生在切换 current 之前**，否则失败会破坏上一版本（回滚就失去意义）。
package deploy

// Kind 是发布模板类型。
type Kind string

const (
	KindCompose Kind = "compose"
	KindSystemd Kind = "systemd"
	KindBinary  Kind = "binary"
)

// 制品类型。
const (
	ArtifactURL    = "url"
	ArtifactUpload = "upload"
	ArtifactImage  = "image"
)

// DefaultKeepReleases 是默认保留的历史 release 目录数量
// （清理时只从第 N+1 个开始删，且 current 与 PrevReleaseID 永远不删）。
const DefaultKeepReleases = 5

// Template 是一个发布模板（Content 为含占位符的模板文本，见 §26）。
//
// 三种类型的 Content 含义：
//   - compose：docker-compose.yml 的内容；
//   - systemd：systemd unit 文件内容；
//   - binary ：可选的启动/重启脚本（空白表示本次发布不需要重启服务）。
type Template struct {
	ID      string
	Kind    Kind
	Content string
}

// Artifact 是要部署的制品。
type Artifact struct {
	Kind      string // "url" | "upload" | "image"
	Location  string // url / 控制端存储 key / 镜像引用
	SHA256    string // url/upload 必填；image 可为空
	SizeBytes int64
}

// Step 是编排出的一个执行步骤，便于记录与排障。
type Step struct {
	Name     string
	Command  string
	TimeoutS int
	// IgnoreFailure 为 true 时该步失败不中断（用于 best-effort 清理）。
	IgnoreFailure bool
}

// Plan 是一次部署在某台服务器上的完整执行计划。
type Plan struct {
	AppSlug   string
	ReleaseID string
	Version   string
	Root      string
	Steps     []Step
	// RollbackSteps 在失败时按逆序执行（best-effort，见 §25.1）。
	//
	// 切片本身按「逆执行顺序」排列：Executor 从后往前跑。
	// 因此「先切回 current、再重启」在切片里写作 [重启, 切回]，
	// 逆序执行即得到正确顺序；Rollback(p) 也按同样规则把它翻成顺序执行的新计划。
	RollbackSteps []Step
}

// BuildParams 是 Build 的输入（全部来自发布记录，Build 不读库不触网）。
type BuildParams struct {
	AppSlug       string
	ReleaseID     string
	Version       string
	Root          string // 部署根；应用根是 <Root>/<AppSlug>
	PrevReleaseID string // 上一个成功发布，用于回滚
	Now           int64
}
