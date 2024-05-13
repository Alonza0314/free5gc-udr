package sbi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/free5gc/openapi/models"
	udr_context "github.com/free5gc/udr/internal/context"
	"github.com/free5gc/udr/internal/logger"
	"github.com/free5gc/udr/internal/sbi/processor"
	"github.com/free5gc/udr/internal/util"
	"github.com/free5gc/udr/pkg/factory"
	"github.com/free5gc/util/httpwrapper"
	logger_util "github.com/free5gc/util/logger"
	"github.com/gin-gonic/gin"
)

type Udr interface {
	Config() *factory.Config
	Context() *udr_context.UDRContext
}

type Server struct {
	Udr

	httpServer *http.Server
	router     *gin.Engine
	processor *processor.Processor
}

func NewServer(udr Udr, tlsKeyLogPath string) *Server {
	s := &Server{
		Udr: udr,
		processor: processor.NewProcessor(udr),
	}

	s.router = newRouter(s)
	server, err := bindRouter(udr, s.router, tlsKeyLogPath)
	s.httpServer = server

	if err != nil {
		logger.SBILog.Errorf("bind Router Error: %+v", err)
		panic("Server initialization failed")
	}

	return s
}

func (s* Server) Processor() *processor.Processor {
	return s.processor
}

func (s *Server) Run(wg *sync.WaitGroup) {
	logger.SBILog.Info("Starting server...")

	wg.Add(1)
	go func() {
		defer wg.Done()

		err := s.serve()
		if err != http.ErrServerClosed {
			logger.SBILog.Panicf("HTTP server setup failed: %+v", err)
		}
	}()
}

func (s *Server) Shutdown() {
	s.shutdownHttpServer()
}

func (s *Server) shutdownHttpServer() {
	const shutdownTimeout time.Duration = 2 * time.Second

	if s.httpServer == nil {
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	err := s.httpServer.Shutdown(shutdownCtx)
	if err != nil {
		logger.SBILog.Errorf("HTTP server shutdown failed: %+v", err)
	}
}

func bindRouter(udr Udr, router *gin.Engine, tlsKeyLogPath string) (*http.Server, error) {
	sbiConfig := udr.Config().Configuration.Sbi
	bindAddr := fmt.Sprintf("%s:%d", sbiConfig.BindingIPv4, sbiConfig.Port)

	return httpwrapper.NewHttp2Server(bindAddr, tlsKeyLogPath, router)
}

func newRouter(s *Server) *gin.Engine {
	router := logger_util.NewGinWithLogrus(logger.GinLog)
	
	dataRepositoryGroup := router.Group(factory.UdrDrResUriPrefix)
	dataRepositoryGroup.Use(func(c *gin.Context) {
		util.NewRouterAuthorizationCheck(models.ServiceName_NUDR_DR).Check(c, s.Context())
	})
	dataRepositoryRoutes := s.getDataRepositoryRoutes()
	AddService(dataRepositoryGroup, dataRepositoryRoutes)
	return router
}

func (s *Server) unsecureServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) secureServe() error {
	sbiConfig := s.Udr.Config().Configuration.Sbi

	pemPath := sbiConfig.Tls.Pem
	if pemPath == "" {
		pemPath = factory.UdrDefaultCertPemPath
	}

	keyPath := sbiConfig.Tls.Key
	if keyPath == "" {
		keyPath = factory.UdrDefaultPrivateKeyPath
	}

	return s.httpServer.ListenAndServeTLS(pemPath, keyPath)
}

func (s *Server) serve() error {
	sbiConfig := s.Udr.Config().Configuration.Sbi

	switch sbiConfig.Scheme {
	case "http":
		return s.unsecureServe()
	case "https":
		return s.secureServe()
	default:
		return fmt.Errorf("invalid SBI scheme: %s", sbiConfig.Scheme)
	}
}