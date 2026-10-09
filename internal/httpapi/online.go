package httpapi

import (
	"context"
	"net/http"

	"github.com/marmot1024/metricspire/internal/application"
	"github.com/marmot1024/metricspire/internal/model"
)

func (server *Server) handleOnlineQuery(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		server.methodNotAllowed(response, request, http.MethodPost)
		return
	}
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
	server.withTimeout(response, request, server.config.QueryTimeout, func(ctx context.Context) error {
		output, err := server.deps.Online.Execute(ctx, application.QueryScope{Namespace: request.PathValue("namespace"), Context: model.RequestContext{Tenant: principal.Tenant, Principal: principal.Subject, Roles: principal.Roles, RequestID: requestID(request)}}, query)
		if err != nil {
			return err
		}
		return writeJSON(response, http.StatusOK, output)
	})
}
