package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	sdklark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

type APIRequest struct {
	Method  httpMethod
	Path    string
	Params  map[string]any
	Data    any
	As      Identity
	Timeout time.Duration
}

type httpMethod = string

type ClientConfig struct {
	AppID           string
	AppSecret       string
	UserAccessToken string
	BaseURL         string
	Timeout         time.Duration
}

type Client struct {
	sdk             *sdklark.Client
	appID           string
	userAccessToken string
	timeout         time.Duration
}

func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.AppID == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark app_id is not configured").WithField("lark.app_id")
	}
	if cfg.AppSecret == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark app secret is not configured").
			WithHint("run `lark-agent auth login` to store credentials in Keychain")
	}
	options := []sdklark.ClientOptionFunc{}
	if cfg.BaseURL != "" {
		options = append(options, sdklark.WithOpenBaseUrl(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		options = append(options, sdklark.WithReqTimeout(cfg.Timeout))
	}
	return &Client{
		sdk:             sdklark.NewClient(cfg.AppID, cfg.AppSecret, options...),
		appID:           cfg.AppID,
		userAccessToken: cfg.UserAccessToken,
		timeout:         cfg.Timeout,
	}, nil
}

func (c *Client) AppID() string {
	if c == nil {
		return ""
	}
	return c.appID
}

func (c *Client) CallAPI(ctx context.Context, request APIRequest) (any, error) {
	if c == nil || c.sdk == nil {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark SDK client is not configured")
	}
	if request.Method == "" || request.Path == "" {
		return nil, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"lark API method and path are required",
		)
	}
	tokenType := larkcore.AccessTokenTypeTenant
	options := []larkcore.RequestOptionFunc{}
	switch request.As {
	case "", IdentityBot:
	case IdentityUser:
		if c.userAccessToken == "" {
			return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark user access token is not configured").
				WithHint("run `lark-agent auth login` before user-identity operations")
		}
		tokenType = larkcore.AccessTokenTypeUser
		options = append(options, larkcore.WithUserAccessToken(c.userAccessToken))
	default:
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported lark identity %q", request.As).
			WithParam("identity")
	}
	ctx, cancel := contextWithOptionalTimeout(ctx, firstDuration(request.Timeout, c.timeout))
	defer cancel()
	resp, err := c.sdk.Do(ctx, &larkcore.ApiReq{
		HttpMethod:                request.Method,
		ApiPath:                   request.Path,
		Body:                      request.Data,
		QueryParams:               queryParams(request.Params),
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{tokenType},
	}, options...)
	if err != nil {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkTransport, "call lark API %s %s", request.Method, request.Path).WithCause(err)
	}
	if resp == nil {
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "lark API response is empty")
	}
	var output map[string]any
	if err := json.Unmarshal(resp.RawBody, &output); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "decode lark API response").WithCause(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiProblem(resp.StatusCode, output, request.As)
	}
	if err := requireSuccessCode(output, request.As); err != nil {
		return nil, err
	}
	return output, nil
}

func queryParams(params map[string]any) larkcore.QueryParams {
	out := larkcore.QueryParams{}
	for key, value := range params {
		switch v := value.(type) {
		case nil:
		case []string:
			for _, item := range v {
				out.Add(key, item)
			}
		case []any:
			for _, item := range v {
				out.Add(key, fmt.Sprint(item))
			}
		default:
			out.Set(key, fmt.Sprint(v))
		}
	}
	return out
}

func numericCode(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		if v == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(v)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func requireSuccessCode(output map[string]any, identity Identity) error {
	code, ok := numericCode(output["code"])
	if !ok {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "lark API response is missing code")
	}
	if code != 0 {
		return apiProblem(code, output, identity)
	}
	return nil
}

func apiProblem(code int, body map[string]any, identity Identity) error {
	message, _ := body["msg"].(string)
	if message == "" {
		message, _ = body["message"].(string)
	}
	if message == "" {
		message = "lark API returned an error"
	}
	return errs.NewAPIError(errs.SubtypeServerError, "%s", message).
		WithCode(code).
		WithIdentity(identity)
}

func contextWithOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func firstDuration(values ...time.Duration) time.Duration {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func ValidateMethod(method string) error {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return nil
	default:
		return fmt.Errorf("unsupported HTTP method %q", method)
	}
}
