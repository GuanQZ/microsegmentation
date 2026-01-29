package controller

import (
    "encoding/json"
    "log"
    "net/http"
    "strings"
)

// APIServer 负责对外提供策略管理接口。
// 变量说明：
// - store: 策略存储（内存/可选文件持久化）
// - token: 可选访问令牌，若设置则要求请求头包含 X-API-Token
type APIServer struct {
    store *PolicyStore
    token string
}

// NewAPIServer 创建 API 服务器实例。
func NewAPIServer(store *PolicyStore, token string) *APIServer {
    return &APIServer{store: store, token: token}
}

// Handler 返回 HTTP 处理器。
// 说明：
// - GET /policy: 获取当前策略
// - PUT /policy: 更新策略（请求体为 PolicyConfig JSON）
func (s *APIServer) Handler() http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", s.handleHealthz)
    mux.HandleFunc("/policy", s.handlePolicy)
    mux.HandleFunc("/apply", s.handleApply)
    return mux
}

// handleHealthz 健康检查接口
func (s *APIServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("ok"))
}

// handlePolicy 处理策略读写
func (s *APIServer) handlePolicy(w http.ResponseWriter, r *http.Request) {
    if !s.authorized(r) {
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte("unauthorized"))
        return
    }

    switch r.Method {
    case http.MethodGet:
        cfg := s.store.Get()
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(cfg)
        return
    default:
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }
}

// handleApply 处理策略下发（POST /apply）
func (s *APIServer) handleApply(w http.ResponseWriter, r *http.Request) {
    if !s.authorized(r) {
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte("unauthorized"))
        return
    }

    if r.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }

    var cfg PolicyConfig
    if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("invalid json"))
        return
    }
    if err := s.store.Set(cfg); err != nil {
        log.Printf("set policy error: %v", err)
        w.WriteHeader(http.StatusInternalServerError)
        _, _ = w.Write([]byte("set policy failed"))
        return
    }
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write([]byte("ok"))
    return
}

// authorized 根据 X-API-Token 头进行简单鉴权。
// 说明：若 token 为空，则不启用鉴权（便于内网测试）。
func (s *APIServer) authorized(r *http.Request) bool {
    if strings.TrimSpace(s.token) == "" {
        return true
    }
    return r.Header.Get("X-API-Token") == s.token
}
