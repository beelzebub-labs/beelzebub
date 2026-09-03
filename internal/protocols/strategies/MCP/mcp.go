package MCP

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/beelzebub-labs/beelzebub/v3/internal/parser"
	"github.com/beelzebub-labs/beelzebub/v3/internal/tracer"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const deployDeployToolName = "beelzebub:deploy"

type remoteAddrCtxKey struct{}

type DeployFunc func(cfg parser.BeelzebubServiceConfiguration) error

type MCPStrategy struct {
	servers   map[string]*server.StreamableHTTPServer
	listeners map[string]net.Listener
	deployFn  DeployFunc
}

func (mcpStrategy *MCPStrategy) SetDeployFn(fn DeployFunc) {
	mcpStrategy.deployFn = fn
}

func (mcpStrategy *MCPStrategy) Init(servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer) error {
	if oldServer, ok := mcpStrategy.servers[servConf.Address]; ok {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		oldServer.Shutdown(ctx)
		cancel()
	}
	if oldListener, ok := mcpStrategy.listeners[servConf.Address]; ok {
		if err := oldListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Errorf("error closing old MCP listener on %s: %v", servConf.Address, err)
		}
		delete(mcpStrategy.listeners, servConf.Address)
	}

	mcpServer := server.NewMCPServer(
		servConf.Description,
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	for _, toolConfig := range servConf.Tools {
		if toolConfig.Params == nil || len(toolConfig.Params) == 0 {
			log.Errorf("Tool %s has no parameters defined", toolConfig.Name)
			continue
		}

		opts := []mcp.ToolOption{
			mcp.WithDescription(toolConfig.Description),
		}

		if toolConfig.Annotations != nil {
			ann := toolConfig.Annotations
			if ann.Title != "" {
				opts = append(opts, mcp.WithTitleAnnotation(ann.Title))
			}
			if ann.ReadOnlyHint != nil {
				opts = append(opts, mcp.WithReadOnlyHintAnnotation(*ann.ReadOnlyHint))
			}
			if ann.DestructiveHint != nil {
				opts = append(opts, mcp.WithDestructiveHintAnnotation(*ann.DestructiveHint))
			}
			if ann.IdempotentHint != nil {
				opts = append(opts, mcp.WithIdempotentHintAnnotation(*ann.IdempotentHint))
			}
			if ann.OpenWorldHint != nil {
				opts = append(opts, mcp.WithOpenWorldHintAnnotation(*ann.OpenWorldHint))
			}
		}

		for _, param := range toolConfig.Params {
			opts = append(opts,
				mcp.WithString(
					param.Name,
					mcp.Required(),
					mcp.Description(param.Description),
				),
			)
		}

		tool := mcp.NewTool(toolConfig.Name, opts...)

		mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			host, port, _ := net.SplitHostPort(ctx.Value(remoteAddrCtxKey{}).(string))

			if toolConfig.Name == deployDeployToolName && mcpStrategy.deployFn != nil {
				return mcpStrategy.handleDeploy(ctx, request, servConf, tr, host, port)
			}

			tr.TraceEvent(tracer.Event{
				Msg:           "New MCP tool invocation",
				Protocol:      tracer.MCP.String(),
				Status:        tracer.Stateless.String(),
				RemoteAddr:    ctx.Value(remoteAddrCtxKey{}).(string),
				SourceIp:      host,
				SourcePort:    port,
				ID:            uuid.New().String(),
				Description:   servConf.Description,
				Command:       fmt.Sprintf("%s|%s", request.Params.Name, request.Params.Arguments),
				CommandOutput: toolConfig.Handler,
			})
			return mcp.NewToolResultText(toolConfig.Handler), nil
		})
	}

	httpSrv := &http.Server{Addr: servConf.Address}
	httpServer := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithStreamableHTTPServer(httpSrv),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, remoteAddrCtxKey{}, r.RemoteAddr)
		}),
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", httpServer)
	httpSrv.Handler = mux

	listener, err := net.Listen("tcp", servConf.Address)
	if err != nil {
		return fmt.Errorf("error binding MCP server on %s: %w", servConf.Address, err)
	}

	if mcpStrategy.servers == nil {
		mcpStrategy.servers = make(map[string]*server.StreamableHTTPServer)
	}
	if mcpStrategy.listeners == nil {
		mcpStrategy.listeners = make(map[string]net.Listener)
	}
	mcpStrategy.servers[servConf.Address] = httpServer
	mcpStrategy.listeners[servConf.Address] = listener

	go func() {
		if err := httpSrv.Serve(listener); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				log.Debugf("MCP server on %s stopped: %s", servConf.Address, err.Error())
			} else {
				log.Errorf("Failed to start MCP server on %s: %v", servConf.Address, err)
			}
		}
	}()

	return nil
}

func (mcpStrategy *MCPStrategy) StopAll() error {
	var errs []error
	for address, srv := range mcpStrategy.servers {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			log.Errorf("error shutting down MCP server: %s", err.Error())
			errs = append(errs, err)
		}
		cancel()
		if listener, ok := mcpStrategy.listeners[address]; ok {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
	}
	mcpStrategy.servers = nil
	mcpStrategy.listeners = nil
	if len(errs) > 0 {
		return fmt.Errorf("mcp stop errors: %w", errors.Join(errs...))
	}
	return nil
}

func (mcpStrategy *MCPStrategy) Stop(servConf parser.BeelzebubServiceConfiguration) error {
	srv, ok := mcpStrategy.servers[servConf.Address]
	if !ok {
		return nil
	}
	var errs []error
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := srv.Shutdown(ctx); err != nil {
		log.Errorf("error shutting down MCP server on %s: %s", servConf.Address, err.Error())
		errs = append(errs, err)
	}
	cancel()
	if listener, ok := mcpStrategy.listeners[servConf.Address]; ok {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	delete(mcpStrategy.servers, servConf.Address)
	delete(mcpStrategy.listeners, servConf.Address)
	return nil
}

func (mcpStrategy *MCPStrategy) handleDeploy(ctx context.Context, request mcp.CallToolRequest, servConf parser.BeelzebubServiceConfiguration, tr tracer.Tracer, host, port string) (*mcp.CallToolResult, error) {
	remoteAddr := ""
	if ra, ok := ctx.Value(remoteAddrCtxKey{}).(string); ok {
		remoteAddr = ra
	}
	args, ok := request.Params.Arguments.(map[string]interface{})
	if !ok {
		return mcp.NewToolResultText(`{"status":"error","message":"invalid arguments format"}`), nil
	}
	configYAMLraw, ok := args["config_yaml"]
	if !ok {
		return mcp.NewToolResultText(`{"status":"error","message":"missing required parameter 'config_yaml'"}`), nil
	}
	configYAML, ok := configYAMLraw.(string)
	if !ok || configYAML == "" {
		return mcp.NewToolResultText(`{"status":"error","message":"config_yaml must be a string"}`), nil
	}

	var cfg parser.BeelzebubServiceConfiguration
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(`{"status":"error","message":"invalid YAML: %s"}`, err.Error())), nil
	}

	if cfg.Protocol == "" {
		return mcp.NewToolResultText(`{"status":"error","message":"config must include a 'protocol' field"}`), nil
	}

	if err := cfg.CompileCommandRegex(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(`{"status":"error","message":"invalid regex: %s"}`, err.Error())), nil
	}
	if err := cfg.CompileTrustedProxies(); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(`{"status":"error","message":"invalid trustedProxies: %s"}`, err.Error())), nil
	}

	if err := mcpStrategy.deployFn(cfg); err != nil {
		return mcp.NewToolResultText(fmt.Sprintf(`{"status":"error","message":"deploy failed: %s"}`, err.Error())), nil
	}

	tr.TraceEvent(tracer.Event{
		Msg:           "Deployed honeypot via MCP",
		Protocol:      tracer.MCP.String(),
		Status:        tracer.Stateless.String(),
		RemoteAddr:    remoteAddr,
		SourceIp:      host,
		SourcePort:    port,
		ID:            uuid.New().String(),
		Description:   servConf.Description,
		Command:       fmt.Sprintf("%s|%s", request.Params.Name, request.Params.Arguments),
		CommandOutput: fmt.Sprintf("deployed %s on %s", cfg.Protocol, cfg.Address),
	})

	return mcp.NewToolResultText(fmt.Sprintf(`{"status":"success","message":"deployed %s honeypot on %s"}`, cfg.Protocol, cfg.Address)), nil
}
