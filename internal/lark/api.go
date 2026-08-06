package lark

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdklark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/accesstoken/refreshtoken"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

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
	RefreshToken    string
	UserTokenStore  UserTokenStore
	BaseURL         string
	OAuthBaseURL    string
	Timeout         time.Duration
}

type Client struct {
	sdk             *sdklark.Client
	appID           string
	userTokenMu     sync.RWMutex
	userAccessToken string
	refreshToken    string
	userTokenStore  UserTokenStore
	timeout         time.Duration
}

// MessageResourceRequest identifies one image or file attached to a message.
type MessageResourceRequest struct {
	MessageID string
	FileKey   string
	Type      string
	As        Identity
	MaxBytes  int64
}

// MessageResource is a bounded resource body returned by the public Lark SDK.
type MessageResource struct {
	Data     []byte
	FileName string
	TooLarge bool
}

func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.AppID == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark app_id is not configured").WithField("lark.app_id")
	}
	if cfg.AppSecret == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark app secret is not configured").
			WithHint("run `lark-agent auth login` to store credentials in Keychain")
	}
	options := []sdklark.ClientOptionFunc{
		sdklark.WithLogger(newCredentialSafeSDKLogger()),
	}
	if cfg.BaseURL != "" {
		options = append(options, sdklark.WithOpenBaseUrl(cfg.BaseURL))
	}
	if cfg.OAuthBaseURL != "" {
		options = append(options, sdklark.WithOAuthBaseUrl(cfg.OAuthBaseURL))
	}
	if cfg.Timeout > 0 {
		options = append(options, sdklark.WithReqTimeout(cfg.Timeout))
	}
	return &Client{
		sdk:             sdklark.NewClient(cfg.AppID, cfg.AppSecret, options...),
		appID:           cfg.AppID,
		userAccessToken: cfg.UserAccessToken,
		refreshToken:    cfg.RefreshToken,
		userTokenStore:  cfg.UserTokenStore,
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
	switch request.As {
	case "", IdentityBot:
	case IdentityUser:
		accessToken, _ := c.currentUserTokens()
		if accessToken == "" {
			return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "lark user access token is not configured").
				WithHint("run `lark-agent auth login` before user-identity operations")
		}
		result, err := c.callAPIOnce(ctx, request, accessToken)
		if err == nil || !isExpiredUserToken(err) {
			return result, err
		}
		accessToken, err = c.recoverUserToken(ctx, accessToken)
		if err != nil {
			return nil, err
		}
		return c.callAPIOnce(ctx, request, accessToken)
	default:
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported lark identity %q", request.As).
			WithParam("identity")
	}
	return c.callAPIOnce(ctx, request, "")
}

// GetMessageResource downloads one bounded message resource through the
// official public SDK. The caller chooses user or bot identity explicitly.
func (c *Client) GetMessageResource(
	ctx context.Context,
	request MessageResourceRequest,
) (MessageResource, error) {
	if c == nil || c.sdk == nil {
		return MessageResource{}, errs.NewConfigError(
			errs.SubtypeNotConfigured,
			"lark SDK client is not configured",
		)
	}
	if request.MessageID == "" || request.FileKey == "" || request.Type == "" {
		return MessageResource{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"message_id, file_key, and type are required",
		)
	}
	identity := request.As
	if identity == "" {
		identity = IdentityUser
	}
	if identity == IdentityBot {
		return c.getMessageResourceOnce(ctx, request, "")
	}
	if identity != IdentityUser {
		return MessageResource{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"unsupported lark identity %q",
			identity,
		).WithParam("identity")
	}
	accessToken, _ := c.currentUserTokens()
	if accessToken == "" {
		return MessageResource{}, errs.NewConfigError(
			errs.SubtypeNotConfigured,
			"lark user access token is not configured",
		).WithHint("run `lark-agent auth login` before user-identity operations")
	}
	result, err := c.getMessageResourceOnce(ctx, request, accessToken)
	if err == nil || !isExpiredUserToken(err) {
		return result, err
	}
	accessToken, err = c.recoverUserToken(ctx, accessToken)
	if err != nil {
		return MessageResource{}, err
	}
	return c.getMessageResourceOnce(ctx, request, accessToken)
}

func (c *Client) getMessageResourceOnce(
	ctx context.Context,
	request MessageResourceRequest,
	userAccessToken string,
) (MessageResource, error) {
	sdkRequest := larkim.NewGetMessageResourceReqBuilder().
		MessageId(request.MessageID).
		FileKey(request.FileKey).
		Type(request.Type).
		Build()
	options := []larkcore.RequestOptionFunc{}
	if request.As == IdentityUser || (request.As == "" && userAccessToken != "") {
		options = append(options, larkcore.WithUserAccessToken(userAccessToken))
	}
	ctx, cancel := contextWithOptionalTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.sdk.Im.MessageResource.Get(ctx, sdkRequest, options...)
	if err != nil {
		return MessageResource{}, errs.NewNetworkError(
			errs.SubtypeNetworkTransport,
			"get lark message resource",
		).WithCause(err)
	}
	if response == nil {
		return MessageResource{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"lark message resource response is empty",
		)
	}
	if !response.Success() {
		return MessageResource{}, apiProblem(response.Code, map[string]any{
			"msg": response.Msg,
		}, request.As)
	}
	limit := request.MaxBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	data, err := io.ReadAll(io.LimitReader(response.File, limit+1))
	if err != nil {
		return MessageResource{}, errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"read lark message resource",
		).WithCause(err)
	}
	if int64(len(data)) > limit {
		return MessageResource{FileName: response.FileName, TooLarge: true}, nil
	}
	return MessageResource{Data: data, FileName: response.FileName}, nil
}

func (c *Client) callAPIOnce(ctx context.Context, request APIRequest, userAccessToken string) (any, error) {
	tokenType := larkcore.AccessTokenTypeTenant
	options := []larkcore.RequestOptionFunc{}
	if request.As == IdentityUser {
		tokenType = larkcore.AccessTokenTypeUser
		options = append(options, larkcore.WithUserAccessToken(userAccessToken))
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
		code := resp.StatusCode
		if apiCode, ok := numericCode(output["code"]); ok && apiCode != 0 {
			code = apiCode
		}
		return nil, apiProblem(code, output, request.As)
	}
	if err := requireSuccessCode(output, request.As); err != nil {
		return nil, err
	}
	return output, nil
}

func (c *Client) currentUserTokens() (string, string) {
	c.userTokenMu.RLock()
	defer c.userTokenMu.RUnlock()
	return c.userAccessToken, c.refreshToken
}

func (c *Client) recoverUserToken(ctx context.Context, failedAccessToken string) (string, error) {
	c.userTokenMu.Lock()
	defer c.userTokenMu.Unlock()

	if c.userAccessToken != "" && c.userAccessToken != failedAccessToken {
		return c.userAccessToken, nil
	}
	if c.userTokenStore != nil {
		stored, err := c.userTokenStore.LoadUserTokens(ctx)
		if err == nil {
			if stored.RefreshToken != "" {
				c.refreshToken = stored.RefreshToken
			}
			if stored.AccessToken != "" && stored.AccessToken != failedAccessToken {
				c.userAccessToken = stored.AccessToken
				return c.userAccessToken, nil
			}
		}
	}
	if c.refreshToken == "" {
		return "", errs.NewPermissionError(
			errs.SubtypeFailedPrecondition,
			"lark user access token expired and no refresh token is available",
		).WithIdentity(string(IdentityUser)).
			WithHint("run `lark-agent auth login` to renew user authorization")
	}
	request := refreshtoken.NewTokenRequestBuilder().
		RefreshToken(c.refreshToken).
		Build()
	response, err := c.sdk.AccessToken.Refresh(ctx, request)
	if err != nil {
		return "", errs.NewPermissionError(
			errs.SubtypeFailedPrecondition,
			"refresh lark user access token",
		).WithIdentity(string(IdentityUser)).
			WithHint("run `lark-agent auth login` if the refresh token has expired").
			WithCause(err)
	}
	if response == nil || response.Data == nil {
		return "", errs.NewInternalError(errs.SubtypeInvalidResponse, "lark token refresh response is empty")
	}
	accessToken := larkcore.StringValue(response.Data.AccessToken)
	refreshToken := larkcore.StringValue(response.Data.RefreshToken)
	if accessToken == "" || refreshToken == "" {
		return "", errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"lark token refresh response is missing rotated credentials",
		)
	}
	tokens := UserTokens{AccessToken: accessToken, RefreshToken: refreshToken}
	if c.userTokenStore != nil {
		if err := c.userTokenStore.StoreUserTokens(ctx, tokens); err != nil {
			return "", err
		}
	}
	c.userAccessToken = accessToken
	c.refreshToken = refreshToken
	return accessToken, nil
}

func isExpiredUserToken(err error) bool {
	problem, ok := errs.ProblemOf(err)
	if !ok || problem.Identity != string(IdentityUser) {
		return false
	}
	switch problem.Code {
	case http.StatusUnauthorized, 1274011, 99991668:
		return true
	}
	message := strings.ToLower(problem.Message)
	return strings.Contains(message, "token expired") ||
		strings.Contains(message, "token is invalid or expired")
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
	var problem *errs.Problem
	lowerMessage := strings.ToLower(message)
	switch {
	case strings.Contains(lowerMessage, "required one of these privileges"),
		strings.Contains(lowerMessage, "required scope"),
		strings.Contains(lowerMessage, "missing scope"):
		problem = errs.NewPermissionError(errs.SubtypeMissingScope, "%s", message)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		problem = errs.NewPermissionError(errs.SubtypeFailedPrecondition, "%s", message)
	default:
		problem = errs.NewAPIError(errs.SubtypeServerError, "%s", message)
	}
	problem = problem.WithCode(code).WithIdentity(identity)
	if field, description := firstFieldViolation(body); field != "" || description != "" {
		if field != "" {
			problem = problem.WithParam(field)
		}
		if description != "" {
			problem = problem.WithHint("%s", description)
		}
	}
	return problem
}

func firstFieldViolation(body map[string]any) (string, string) {
	errorBody, _ := body["error"].(map[string]any)
	violations, _ := errorBody["field_violations"].([]any)
	if len(violations) == 0 {
		return "", ""
	}
	violation, _ := violations[0].(map[string]any)
	field, _ := violation["field"].(string)
	description, _ := violation["description"].(string)
	return strings.TrimSpace(field), strings.TrimSpace(description)
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
