package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AmbitiousJun/go-emby2openlist/v2/internal/config"
	"github.com/gin-gonic/gin"
)

func TestCacheableRouteMarkerDisablesStreamCacheForStrmPro(t *testing.T) {
	oldConfig := config.C
	t.Cleanup(func() { config.C = oldConfig })

	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "strmpro enabled", enabled: true, want: "-1"},
		{name: "strmpro disabled", enabled: false, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config.C = &config.Config{
				Emby: &config.Emby{
					Strm: &config.Strm{StrmPro: tt.enabled},
				},
			}

			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/videos/123/stream?api_key=test", nil)
			CacheableRouteMarker()(c)

			if got := c.Writer.Header().Get(HeaderKeyExpired); got != tt.want {
				t.Fatalf("Expired header = %q, want %q", got, tt.want)
			}
		})
	}
}
