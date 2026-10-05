package catalogclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	orderserrors "github.com/KolibriRDM/orders-service/internal/errors"
	"github.com/KolibriRDM/orders-service/internal/model"
)

// Переиспользуемый HTTP-клиент внутреннего API каталога.
type Client struct {
	httpClient *http.Client
	resolveURL string
}

// Проверяет адрес каталога и настраивает HTTP-клиент.
func NewClient(baseURL string) (*Client, error) {
	trimURL := strings.TrimSpace(baseURL)
	parsed, err := url.Parse(trimURL)
	if err != nil {
		return nil, errors.New("invalid catalog base URL")
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("catalog URL scheme must be http or https")
	}

	if parsed.Hostname() == "" {
		return nil, errors.New("catalog URL host is required")
	}

	if parsed.User != nil {
		return nil, errors.New("catalog URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(trimURL, "#") {
		return nil, errors.New("catalog URL must not contain query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("catalog URL must not contain a path")
	}

	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("catalog URL port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return nil, errors.New("catalog URL port must not be empty")
	}
	parsed.RawPath = ""
	parsed.Path = "/internal/v1/menu/resolve"
	httpClient := http.Client{
		Timeout: 500 * time.Millisecond,
		// Перенаправление не должно уводить запрос на другой адрес.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Client{httpClient: &httpClient, resolveURL: parsed.String()}, nil
}

// 64 << 10 — это 64 KiB: сдвиг умножает 64 на 1024.
const maxResponseBytes = 64 << 10

// Получает проверенные блюда по уже нормализованным UUID.
func (c *Client) ResolveItems(ctx context.Context, restaurantID string, itemIDs []string) ([]model.MenuItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resolve items: %w", err)
	}
	// Уникальные ID позволяют однозначно сопоставить запрос и ответ.
	if restaurantID == "" || len(itemIDs) < 1 || len(itemIDs) > 20 {
		return nil, fmt.Errorf("invalid catalog resolve parameters: %w", orderserrors.ErrInvalidInput)
	}
	requested := make(map[string]struct{}, len(itemIDs))
	for _, id := range itemIDs {
		if _, exists := requested[id]; exists || id == "" {
			return nil, fmt.Errorf("invalid catalog item IDs: %w", orderserrors.ErrInvalidInput)
		}
		requested[id] = struct{}{}
	}
	requestBody := resolveItemsRequest{
		RestaurantID: restaurantID,
		ItemIDs:      itemIDs,
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode catalog request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resolveURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create catalog request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)

	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("resolve catalog items: %w", orderserrors.ErrCatalogUnavailable)
	}

	defer resp.Body.Close()
	// Дополнительный байт позволяет обнаружить превышение лимита.
	limitedBody := io.LimitReader(resp.Body, maxResponseBytes+1)

	responseBody, err := io.ReadAll(limitedBody)

	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read catalog response: %w", orderserrors.ErrCatalogUnavailable)
	}

	if len(responseBody) > maxResponseBytes {
		return nil, fmt.Errorf("catalog response exceeds size limit: %w", orderserrors.ErrCatalogUnavailable)
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))

	if err != nil || mediaType != "application/json" {
		return nil, fmt.Errorf("unexpected catalog response content type: %w", orderserrors.ErrCatalogUnavailable)
	}

	if resp.StatusCode != http.StatusOK {
		var response errorResponse
		if err := json.Unmarshal(responseBody, &response); err != nil {
			return nil, fmt.Errorf("invalid catalog error response: %w", orderserrors.ErrCatalogUnavailable)
		}
		switch {
		case resp.StatusCode == http.StatusBadRequest && response.Code == "invalid_input":
			return nil, fmt.Errorf("catalog rejected items: %w", orderserrors.ErrInvalidInput)
		case resp.StatusCode == http.StatusNotFound && response.Code == "not_found":
			return nil, fmt.Errorf("catalog restaurant not found: %w", orderserrors.ErrNotFound)
		default:
			return nil, fmt.Errorf("unexpected catalog status %d: %w", resp.StatusCode, orderserrors.ErrCatalogUnavailable)
		}
	}

	var response resolveItemsResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("invalid catalog response JSON: %w", orderserrors.ErrCatalogUnavailable)
	}
	if response.RestaurantID != restaurantID || len(response.Items) != len(itemIDs) {
		return nil, fmt.Errorf("catalog response does not match request: %w", orderserrors.ErrCatalogUnavailable)
	}
	byID := make(map[string]model.MenuItem, len(response.Items))
	for _, item := range response.Items {
		if _, ok := requested[item.ID]; !ok {
			return nil, fmt.Errorf("unexpected catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		if _, exists := byID[item.ID]; exists {
			return nil, fmt.Errorf("duplicate catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		if strings.TrimSpace(item.Name) == "" || item.PriceKopecks < 1 || item.PriceKopecks > int64(model.MaxUnitPrice) {
			return nil, fmt.Errorf("invalid catalog item values: %w", orderserrors.ErrCatalogUnavailable)
		}
		byID[item.ID] = model.MenuItem{
			ID: item.ID, RestaurantID: restaurantID, Name: item.Name,
			PriceKopecks: model.Money(item.PriceKopecks), IsAvailable: true,
		}
	}
	items := make([]model.MenuItem, 0, len(itemIDs))
	for _, id := range itemIDs {
		item, exists := byID[id]
		if !exists {
			return nil, fmt.Errorf("missing catalog item: %w", orderserrors.ErrCatalogUnavailable)
		}
		items = append(items, item)
	}
	return items, nil
}
