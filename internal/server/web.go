package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/clock"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/service"
	"github.com/linsomniac/taskronin/internal/web"
)

// AIDEV-NOTE: This is the only place server learns about web. The lifecycle
// supplies the bound listen address and the service's configured environment.
func init() {
	newWebHandler = func(svc *service.Service, clk clock.Clock, zone civil.Zone, weekStart time.Weekday, listen string, log *slog.Logger) (http.Handler, error) {
		return web.New(web.Options{Svc: svc, Clock: clk, Zone: zone, WeekStart: weekStart, Listen: listen, Logger: log,
			Observe: func(ctx context.Context, route string, actor domain.Actor) {
				if st, ok := ctx.Value(requestStateKey{}).(*requestState); ok {
					st.route, st.actor = route, actor
				}
			}})
	}
}
