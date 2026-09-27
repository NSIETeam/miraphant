package main

import (
	"embed"
	"fmt"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/controller"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/router"
	"github.com/songquanpeng/one-api/service/payments"
	"net"
	"os"
	"strconv"
)

//go:embed web/build/*
var buildFS embed.FS

func main() {
	common.Init()
	logger.SetupLogger()
	logger.SysLogf("One API %s started", common.Version)

	if os.Getenv("GIN_MODE") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}
	if config.DebugEnabled {
		logger.SysLog("running in debug mode")
	}

	// Initialize SQL Database
	model.InitDB()
	if *common.RecoverPointHolds && *common.MigratePoints {
		logger.FatalLog("--recover-point-holds and --migrate-points cannot be used together")
	}
	if *common.RecoverPointHolds {
		if *common.PointsRecoveryBatch == "" || *common.PointsRecoveryReason == "" {
			logger.FatalLog("offline recovery requires --points-recovery-batch and --points-recovery-reason")
		}
		if !common.UsingSQLite {
			logger.FatalLog("offline held-request recovery currently supports the single-instance SQLite deployment only")
		}
		if err := model.RequirePointsSchema(); err != nil {
			logger.FatalLog("points schema is required for recovery: " + err.Error())
		}
		recovered, err := model.RecoverOrphanedPointHolds(*common.PointsRecoveryBatch, *common.PointsRecoveryReason)
		if err != nil {
			logger.FatalLog("offline point hold recovery failed: " + err.Error())
		}
		if err := model.CloseDB(); err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
		logger.SysLogf("offline point hold recovery completed; marked %d held request(s) pending", recovered)
		return
	}
	model.InitLogDB()
	if *common.MigratePoints {
		if err := model.MigratePointsSchema(); err != nil {
			logger.FatalLog("points schema migration failed: " + err.Error())
		}
		logger.SysLog("points schema migration completed")
		if err := model.CloseDB(); err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
		return
	}
	if config.PointsBillingEnabled || config.WeChatPayEnabled || config.AlipayConfigured {
		if err := model.RequirePointsSchema(); err != nil {
			logger.FatalLog("points or payment processing is configured but its schema is not ready")
		}
	}

	var err error
	err = model.CreateRootAccountIfNeed()
	if err != nil {
		logger.FatalLog("database init error: " + err.Error())
	}
	defer func() {
		err := model.CloseDB()
		if err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
	}()

	// Initialize Redis
	err = common.InitRedisClient()
	if err != nil {
		logger.FatalLog("failed to initialize Redis: " + err.Error())
	}

	// Initialize options
	model.InitOptionMap()
	if err := payments.ConfigureProviders(); err != nil {
		logger.FatalLog("payment provider configuration is invalid")
	}
	logger.SysLog(fmt.Sprintf("using theme %s", config.Theme))
	if common.RedisEnabled {
		// for compatibility with old versions
		config.MemoryCacheEnabled = true
	}
	if config.MemoryCacheEnabled {
		logger.SysLog("memory cache enabled")
		logger.SysLog(fmt.Sprintf("sync frequency: %d seconds", config.SyncFrequency))
		model.InitChannelCache()
	}
	if config.MemoryCacheEnabled {
		go model.SyncOptions(config.SyncFrequency)
		go model.SyncChannelCache(config.SyncFrequency)
	}
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err != nil {
			logger.FatalLog("failed to parse CHANNEL_TEST_FREQUENCY: " + err.Error())
		}
		go controller.AutomaticallyTestChannels(frequency)
	}
	if os.Getenv("BATCH_UPDATE_ENABLED") == "true" {
		config.BatchUpdateEnabled = true
		logger.SysLog("batch update enabled with interval " + strconv.Itoa(config.BatchUpdateInterval) + "s")
		model.InitBatchUpdater()
	}
	if config.EnableMetric {
		logger.SysLog("metric enabled, will disable channel if too much request failed")
	}
	openai.InitTokenEncoders()
	client.Init()

	// Initialize HTTP server
	server := gin.New()
	server.Use(gin.Recovery())
	// This will cause SSE not to work!!!
	//server.Use(gzip.Gzip(gzip.DefaultCompression))
	server.Use(middleware.RequestId())
	middleware.SetUpLogger(server)
	// Initialize session store
	store := cookie.NewStore([]byte(config.SessionSecret))
	server.Use(sessions.Sessions("session", store))

	router.SetRouter(server, buildFS)
	var port = os.Getenv("PORT")
	if port == "" {
		port = strconv.Itoa(*common.Port)
	}
	logger.SysLogf("server started on http://localhost:%s", port)
	address := os.Getenv("BIND_ADDRESS")
	if address == "" {
		address = ":" + port // preserve the upstream all-interface default
	} else if _, _, splitErr := net.SplitHostPort(address); splitErr != nil {
		address = net.JoinHostPort(address, port)
	}
	err = server.Run(address)
	if err != nil {
		logger.FatalLog("failed to start HTTP server: " + err.Error())
	}
}
