package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

var (
	dbIdentifierRe = regexp.MustCompile(`^[A-Za-z0-9_$-]{1,64}$`)
	dbHostRe       = regexp.MustCompile(`^[A-Za-z0-9.:_-]{1,255}$`)
)

type databaseBackupRequest struct {
	Engine   string `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

// handleDatabaseBackup 处理 POST /servers/{id}/database/backup。
//
// 在目标机上执行 mysqldump（通过 agent/SSH 通道），把 SQL 直接作为附件返回。
// 密码只放在进程环境变量 MYSQL_PWD，不出现在 argv；响应与审计都不回显密码。
func (a *API) handleDatabaseBackup(w http.ResponseWriter, r *http.Request) {
	serverID := strings.TrimSpace(chi.URLParam(r, "serverID"))
	if serverID == "" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "缺少服务器 ID")
		return
	}
	if a.remoteExec == nil {
		writeError(w, http.StatusServiceUnavailable, codeInternal, "远程执行通道未启用，无法备份数据库")
		return
	}
	var req databaseBackupRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "请求体格式错误: "+err.Error())
		return
	}
	if strings.ToLower(strings.TrimSpace(req.Engine)) != "mysql" {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "目前只支持 MySQL/MariaDB 备份（engine=mysql）")
		return
	}
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	user := strings.TrimSpace(req.User)
	database := strings.TrimSpace(req.Database)
	if !dbHostRe.MatchString(host) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库地址格式不合法")
		return
	}
	if !dbIdentifierRe.MatchString(user) || !dbIdentifierRe.MatchString(database) {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库用户名或库名格式不合法")
		return
	}
	port := req.Port
	if port == 0 {
		port = 3306
	}
	if port < 1 || port > 65535 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库端口必须在 1-65535 之间")
		return
	}
	if len(req.Password) > 256 {
		writeError(w, http.StatusBadRequest, codeInvalidArgument, "数据库密码过长")
		return
	}
	// 单引号闭合转义；不把密码拼进 argv，避免 ps 泄露。
	escapedPassword := strings.ReplaceAll(req.Password, "'", `'\''`)
	command := fmt.Sprintf(
		"MYSQL_PWD='%s' mysqldump --single-transaction --quick --skip-lock-tables --default-character-set=utf8mb4 -h '%s' -P %d -u '%s' --databases '%s'",
		escapedPassword, host, port, user, database,
	)
	stdout, stderr, exitCode, err := a.remoteExec.Exec(r.Context(), serverID, command, 300)
	if err != nil {
		writeError(w, http.StatusBadGateway, codeInternal, "数据库备份执行失败: "+err.Error())
		return
	}
	if exitCode != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("mysqldump 退出码 %d", exitCode)
		}
		writeError(w, http.StatusBadGateway, codeInternal, "数据库备份失败: "+msg)
		return
	}
	if strings.TrimSpace(stdout) == "" {
		writeError(w, http.StatusBadGateway, codeInternal, "mysqldump 没有返回 SQL 内容")
		return
	}
	a.auditManual(r, "server.database_backup", "server", serverID,
		fmt.Sprintf(`{"engine":"mysql","database":%q,"bytes":%d}`, database, len(stdout)))
	filename := fmt.Sprintf("%s-%s.sql", database, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/sql; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(stdout)))
	if _, werr := w.Write([]byte(stdout)); werr != nil {
		a.logger.Warn("写出数据库备份失败", "server_id", serverID, "err", werr)
	}
}
