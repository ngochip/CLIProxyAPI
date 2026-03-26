package openai

import (
    "context"
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/gin-gonic/gin"
    "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
    coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
    sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
    "github.com/tidwall/gjson"
)

type chatCaptureExecutor struct {
    payload      []byte
    sourceFormat string
    calls        int
}

func (e *chatCaptureExecutor) Identifier() string { return "test-provider" }

func (e *chatCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
    e.calls++
    e.payload = append([]byte(nil), req.Payload...)
    e.sourceFormat = opts.SourceFormat.String()
    return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *chatCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
    return nil, errors.New("not implemented")
}

func (e *chatCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
    return auth, nil
}

func (e *chatCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
    return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *chatCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
    return nil, errors.New("not implemented")
}

func TestChatCompletions_ResponsesPayloadPreservesToolsFilesAndLinks(t *testing.T) {
    gin.SetMode(gin.TestMode)
    executor := &chatCaptureExecutor{}
    manager := coreauth.NewManager(nil, nil, nil)
    manager.RegisterExecutor(executor)

    auth := &coreauth.Auth{ID: "auth-chat-1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
    if _, err := manager.Register(context.Background(), auth); err != nil {
        t.Fatalf("Register auth: %v", err)
    }
    registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
    t.Cleanup(func() {
        registry.GetGlobalRegistry().UnregisterClient(auth.ID)
    })

    base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
    h := NewOpenAIAPIHandler(base)
    router := gin.New()
    router.POST("/v1/chat/completions", h.ChatCompletions)

    body := `{
      "model":"test-model",
      "stream":false,
      "input":[{
        "role":"user",
        "content":[
          {"type":"input_text","text":"hi"},
          {"type":"input_image","image_url":"https://example.com/image.png"},
          {"type":"input_file","file_id":"file-123","filename":"spec.pdf"}
        ]
      }],
      "tools":[
        {"type":"web_search","search_context_size":"low"},
        {"type":"custom","name":"ApplyPatch","description":"patch tool","format":{"type":"grammar","syntax":"lark"}},
        {"type":"function","name":"ReadFile","description":"read file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"strict":false}
      ]
    }`

    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    resp := httptest.NewRecorder()
    router.ServeHTTP(resp, req)

    if resp.Code != http.StatusOK {
        t.Fatalf("status = %d, want %d, body=%s", resp.Code, http.StatusOK, resp.Body.String())
    }
    if executor.calls != 1 {
        t.Fatalf("executor calls = %d, want 1", executor.calls)
    }
    if executor.sourceFormat != "openai" {
        t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai")
    }

    forwarded := executor.payload
    if got := gjson.GetBytes(forwarded, "tools.#").Int(); got != 3 {
        t.Fatalf("forwarded tools len = %d, want 3: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "tools.0.type").String(); got != "web_search" {
        t.Fatalf("forwarded tools[0].type = %q, want web_search: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "tools.1.type").String(); got != "custom" {
        t.Fatalf("forwarded tools[1].type = %q, want custom: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "tools.1.name").String(); got != "ApplyPatch" {
        t.Fatalf("forwarded tools[1].name = %q, want ApplyPatch: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "tools.2.function.strict").Bool(); got != false {
        t.Fatalf("forwarded function strict = %v, want false: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "messages.0.content.1.image_url.url").String(); got != "https://example.com/image.png" {
        t.Fatalf("forwarded image url = %q, want preserved: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "messages.0.content.2.file.file_id").String(); got != "file-123" {
        t.Fatalf("forwarded file_id = %q, want preserved: %s", got, forwarded)
    }
    if got := gjson.GetBytes(forwarded, "messages.0.content.2.file.filename").String(); got != "spec.pdf" {
        t.Fatalf("forwarded filename = %q, want preserved: %s", got, forwarded)
    }
}
