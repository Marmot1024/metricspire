package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/model"
)

func (server *Server) handleOnlineQuery(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), server.config.OnlineTimeout)
	defer cancel()
	request = request.WithContext(ctx)
	// Bound slow body reads on real net/http connections as well as database
	// dependencies. Recorders may not support read deadlines.
	controller := http.NewResponseController(response)
	deadline, _ := ctx.Deadline()
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		server.handleError(response, request, err)
		return
	}
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	principal, ok := server.authorize(response, request, PermissionQuery)
	if !ok {
		return
	}
	if server.deps.Online == nil {
		server.writeProblem(response, request, http.StatusNotFound, "online_not_enabled", "Online query unavailable", "online serving is not enabled", "")
		return
	}
	var query application.OnlineQuery
	if !server.decodeJSON(response, request, &query) {
		return
	}
	output, err := server.deps.Online.Execute(ctx, application.QueryScope{Namespace: request.PathValue("namespace"), Context: model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)}}, query)
	if contextErr := requestContextError(request); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		server.handleError(response, request, err)
		return
	}
	if err := writeJSON(response, http.StatusOK, output); err != nil {
		server.logger.Error("write online response", "request_id", requestID(request))
	}
}
