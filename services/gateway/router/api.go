package router

import (
	"github.com/songquanpeng/one-api/controller"
	"github.com/songquanpeng/one-api/controller/auth"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

func SetApiRouter(router *gin.Engine) {
	callbacks := router.Group("/api/payments/notify")
	callbacks.Use(middleware.PaymentCallbackRateLimit())
	callbacks.POST("/wechat", controller.WeChatPaymentNotify)
	callbacks.POST("/alipay", controller.AlipayPaymentNotify)
	refundCallbacks := router.Group("/api/payments/refunds/notify")
	refundCallbacks.Use(middleware.PaymentCallbackRateLimit())
	refundCallbacks.POST("/wechat", controller.WeChatRefundNotify)

	apiRouter := router.Group("/api")
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	{
		apiRouter.GET("/status", controller.GetStatus)
		apiRouter.GET("/models", middleware.UserAuth(), controller.DashboardListModels)
		apiRouter.GET("/notice", controller.GetNotice)
		apiRouter.GET("/about", controller.GetAbout)
		apiRouter.GET("/home_page_content", controller.GetHomePageContent)
		apiRouter.GET("/payments/packages", controller.PaymentPackages)
		paymentUser := apiRouter.Group("/payments")
		paymentUser.Use(middleware.PointsUserAuth())
		{
			paymentUser.GET("/options", controller.PaymentOptions)
			paymentUser.GET("/csrf", controller.PaymentsCSRF)
			paymentUser.POST("/orders", middleware.PaymentUserWriteRateLimit(), middleware.PointsCSRF(), controller.CreatePointPurchaseOrder)
			paymentUser.GET("/orders", controller.PointPurchaseOrders)
			paymentUser.GET("/orders/:key", controller.PointPurchaseOrder)
			paymentUser.GET("/orders/:key/checkout", controller.PointPurchaseOrderCheckout)
			paymentUser.GET("/orders/:key/refund-quote", controller.CustomerPointRefundQuote)
			paymentUser.GET("/orders/:key/refunds", controller.CustomerPointRefundsForOrder)
			paymentUser.POST("/orders/:key/refund-requests", middleware.PaymentUserWriteRateLimit(), middleware.RefundStepUpBodyLimit(), middleware.PointsCSRF(), controller.CreateCustomerPointRefund)
			paymentUser.GET("/refunds/:key", controller.CustomerPointRefund)
			paymentUser.POST("/orders/:key/query", middleware.PaymentUserWriteRateLimit(), middleware.PointsCSRF(), controller.PointPurchaseOrderQuery)
			paymentUser.POST("/orders/:key/close", middleware.PaymentUserWriteRateLimit(), middleware.PointsCSRF(), controller.PointPurchaseOrderClose)
		}
		paymentAdmin := apiRouter.Group("/admin/points/payments")
		paymentAdmin.Use(middleware.PointsAdminAuth())
		{
			paymentAdmin.GET("/status", controller.AdminPaymentStatus)
			paymentAdmin.GET("/packages", controller.AdminPointPackages)
			paymentAdmin.POST("/packages", middleware.PointsCSRF(), controller.CreatePointPackage)
			paymentAdmin.POST("/recover", middleware.PointsCSRF(), controller.AdminRecoverPaymentEvents)
		}
		paymentOpsAdmin := apiRouter.Group("/admin")
		paymentOpsAdmin.Use(middleware.PointsAdminAuth())
		{
			paymentOpsAdmin.GET("/orders", controller.AdminPaymentOrders)
			paymentOpsAdmin.GET("/orders/:key", controller.AdminPaymentOrder)
			paymentOpsAdmin.GET("/audit", controller.AdminPaymentAudit)
		}
		refundAuth := apiRouter.Group("/refund-auth")
		refundAuth.Use(middleware.RefundSessionAuth())
		{
			refundAuth.GET("/csrf", controller.RefundAuthorizationCSRF)
			refundAuth.GET("/self", controller.RefundAuthorizationSelf)
			refundAuth.POST("/step-up", middleware.CriticalRateLimit(), middleware.RefundStepUpIPRateLimit(), middleware.RefundStepUpUserRateLimit(), middleware.RefundStepUpBodyLimit(), middleware.PointsCSRF(), controller.IssueRefundAuthorizationStepUp)
		}
		refundAuthAdmin := apiRouter.Group("/admin/refund-auth")
		refundAuthAdmin.Use(middleware.RefundSessionAuth(), middleware.RefundManagerAuth())
		{
			refundAuthAdmin.GET("/users/:id/grants", controller.AdminRefundCapabilityGrants)
			refundAuthAdmin.POST("/grants", middleware.RefundStepUpBodyLimit(), middleware.PointsCSRF(), controller.ChangeRefundCapabilities)
		}
		refundAdmin := apiRouter.Group("/admin/refunds")
		refundAdmin.Use(middleware.RefundSessionAuth())
		{
			refundAdmin.GET("", middleware.RefundCapabilityAuth(model.RefundCapabilityRead), controller.AdminPointRefunds)
			refundAdmin.GET("/:key", middleware.RefundCapabilityAuth(model.RefundCapabilityRead), controller.AdminPointRefund)
			refundAdmin.POST("/:key/approve", middleware.RefundStepUpBodyLimit(), middleware.RefundStepUpUserRateLimit(), middleware.RefundStepUpIPRateLimit(), middleware.PointsCSRF(), middleware.RefundCapabilityAuth(model.RefundCapabilityReview), controller.DecideAdminPointRefund)
			refundAdmin.POST("/:key/reject", middleware.RefundStepUpBodyLimit(), middleware.RefundStepUpUserRateLimit(), middleware.RefundStepUpIPRateLimit(), middleware.PointsCSRF(), middleware.RefundCapabilityAuth(model.RefundCapabilityReview), controller.DecideAdminPointRefund)
			refundAdmin.POST("/:key/submit", middleware.RefundStepUpBodyLimit(), middleware.RefundStepUpUserRateLimit(), middleware.RefundStepUpIPRateLimit(), middleware.PointsCSRF(), middleware.RefundCapabilityAuth(model.RefundCapabilitySubmit), controller.SubmitAdminPointRefund)
			refundAdmin.POST("/:key/reconcile", middleware.PaymentUserWriteRateLimit(), middleware.PointsCSRF(), middleware.RefundCapabilityAuth(model.RefundCapabilityReconcile), controller.ReconcileAdminPointRefund)
		}
		reconciliationAdmin := apiRouter.Group("/admin/reconciliation")
		reconciliationAdmin.Use(middleware.RefundSessionAuth())
		{
			reconciliationAdmin.GET("/status", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationStatus)
			reconciliationAdmin.GET("/batches", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationBatches)
			reconciliationAdmin.GET("/batches/:key", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationBatch)
			reconciliationAdmin.GET("/batches/:key/rows", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationRows)
			reconciliationAdmin.GET("/batches/:key/differences", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationDifferences)
			reconciliationAdmin.GET("/differences/:id/actions", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationActions)
			reconciliationAdmin.GET("/import-attempts", middleware.RefundCapabilityAuth(model.ReconciliationCapabilityRead), controller.AdminReconciliationImportAudits)
			reconciliationAdmin.POST("/import", middleware.RefundStepUpBodyLimit(), middleware.RefundCapabilityAuth(model.ReconciliationCapabilityImport), middleware.PointsCSRF(), middleware.ReconciliationImportRateLimit(), controller.AdminImportReconciliationBill)
			reconciliationAdmin.POST("/differences/:id/actions", middleware.RefundStepUpBodyLimit(), middleware.RefundCapabilityAuth(model.ReconciliationCapabilityNote), middleware.PointsCSRF(), middleware.PaymentUserWriteRateLimit(), controller.AdminRecordReconciliationAction)
		}
		apiRouter.GET("/verification", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification)
		apiRouter.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail)
		apiRouter.POST("/user/reset", middleware.CriticalRateLimit(), controller.ResetPassword)
		apiRouter.GET("/oauth/github", middleware.CriticalRateLimit(), auth.GitHubOAuth)
		apiRouter.GET("/oauth/oidc", middleware.CriticalRateLimit(), auth.OidcAuth)
		apiRouter.GET("/oauth/lark", middleware.CriticalRateLimit(), auth.LarkOAuth)
		apiRouter.GET("/oauth/state", middleware.CriticalRateLimit(), auth.GenerateOAuthCode)
		apiRouter.GET("/oauth/wechat", middleware.CriticalRateLimit(), auth.WeChatAuth)
		apiRouter.GET("/oauth/wechat/bind", middleware.CriticalRateLimit(), middleware.UserAuth(), auth.WeChatBind)
		apiRouter.GET("/oauth/email/bind", middleware.CriticalRateLimit(), middleware.UserAuth(), controller.EmailBind)
		apiRouter.POST("/topup", middleware.AdminAuth(), controller.AdminTopUp)
		publicPointsRoute := apiRouter.Group("/points")
		publicPointsRoute.Use(middleware.PointsBillingAvailable())
		publicPointsRoute.GET("/prices", controller.PointsPrices)
		pointsRoute := apiRouter.Group("/points")
		pointsRoute.Use(middleware.PointsBillingAvailable(), middleware.PointsUserAuth())
		{
			pointsRoute.GET("/csrf", controller.PointsCSRF)
			pointsRoute.GET("/wallet", controller.PointsWallet)
			pointsRoute.POST("/estimate", middleware.CriticalRateLimit(), controller.PointsEstimate)
			pointsRoute.GET("/usage", controller.PointsUsage)
			pointsRoute.GET("/tokens", controller.PointsTokens)
			pointsRoute.PUT("/tokens/:id/budget", middleware.PointsCSRF(), controller.SetPointsTokenBudget)
			pointsRoute.PUT("/tokens/:id/settings", middleware.PointsCSRF(), controller.SetPointsTokenSettings)
		}
		pointsAdmin := apiRouter.Group("/admin/points")
		pointsAdmin.Use(middleware.PointsBillingAvailable(), middleware.PointsAdminAuth())
		{
			pointsAdmin.POST("/prices", middleware.PointsCSRF(), controller.AdminPublishPointPrice)
			pointsAdmin.POST("/adjustments", middleware.PointsCSRF(), controller.AdminGrantPoints)
			pointsAdmin.GET("/pending", controller.AdminPointPending)
			pointsAdmin.POST("/pending/:key/resolve", middleware.PointsCSRF(), controller.AdminResolvePointHold)
		}

		userRoute := apiRouter.Group("/user")
		{
			userRoute.POST("/register", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.Register)
			userRoute.POST("/login", middleware.CriticalRateLimit(), controller.Login)
			userRoute.GET("/logout", controller.Logout)

			selfRoute := userRoute.Group("/")
			selfRoute.Use(middleware.UserAuth())
			{
				selfRoute.GET("/dashboard", controller.GetUserDashboard)
				selfRoute.GET("/self", controller.GetSelf)
				selfRoute.PUT("/self", controller.UpdateSelf)
				selfRoute.DELETE("/self", controller.DeleteSelf)
				selfRoute.GET("/token", controller.GenerateAccessToken)
				selfRoute.GET("/aff", controller.GetAffCode)
				selfRoute.POST("/topup", controller.TopUp)
				selfRoute.GET("/available_models", controller.GetUserAvailableModels)
			}

			adminRoute := userRoute.Group("/")
			adminRoute.Use(middleware.AdminAuth())
			{
				adminRoute.GET("/", controller.GetAllUsers)
				adminRoute.GET("/search", controller.SearchUsers)
				adminRoute.GET("/:id", controller.GetUser)
				adminRoute.POST("/", controller.CreateUser)
				adminRoute.POST("/manage", controller.ManageUser)
				adminRoute.PUT("/", controller.UpdateUser)
				adminRoute.DELETE("/:id", controller.DeleteUser)
			}
		}
		optionRoute := apiRouter.Group("/option")
		optionRoute.Use(middleware.RootAuth())
		{
			optionRoute.GET("/", controller.GetOptions)
			optionRoute.PUT("/", controller.UpdateOption)
		}
		channelRoute := apiRouter.Group("/channel")
		channelRoute.Use(middleware.AdminAuth())
		{
			channelRoute.GET("/", controller.GetAllChannels)
			channelRoute.GET("/search", controller.SearchChannels)
			channelRoute.GET("/models", controller.ListAllModels)
			channelRoute.GET("/:id", controller.GetChannel)
			channelRoute.GET("/test", controller.TestChannels)
			channelRoute.GET("/test/:id", controller.TestChannel)
			channelRoute.GET("/update_balance", controller.UpdateAllChannelsBalance)
			channelRoute.GET("/update_balance/:id", controller.UpdateChannelBalance)
			channelRoute.POST("/", controller.AddChannel)
			channelRoute.PUT("/", controller.UpdateChannel)
			channelRoute.DELETE("/disabled", controller.DeleteDisabledChannel)
			channelRoute.DELETE("/:id", controller.DeleteChannel)
		}
		tokenRoute := apiRouter.Group("/token")
		tokenRoute.Use(middleware.UserAuth())
		{
			tokenRoute.GET("/", middleware.BlockLegacyTokenReadInPointsMode(), controller.GetAllTokens)
			tokenRoute.GET("/search", middleware.BlockLegacyTokenReadInPointsMode(), controller.SearchTokens)
			tokenRoute.GET("/:id", middleware.BlockLegacyTokenReadInPointsMode(), controller.GetToken)
			tokenRoute.POST("/", middleware.PointsModeCSRF(), controller.AddToken)
			tokenRoute.PUT("/", middleware.PointsModeCSRF(), controller.UpdateToken)
			tokenRoute.DELETE("/:id", middleware.PointsModeCSRF(), controller.DeleteToken)
		}
		redemptionRoute := apiRouter.Group("/redemption")
		redemptionRoute.Use(middleware.AdminAuth())
		{
			redemptionRoute.GET("/", controller.GetAllRedemptions)
			redemptionRoute.GET("/search", controller.SearchRedemptions)
			redemptionRoute.GET("/:id", controller.GetRedemption)
			redemptionRoute.POST("/", controller.AddRedemption)
			redemptionRoute.PUT("/", controller.UpdateRedemption)
			redemptionRoute.DELETE("/:id", controller.DeleteRedemption)
		}
		logRoute := apiRouter.Group("/log")
		logRoute.GET("/", middleware.AdminAuth(), controller.GetAllLogs)
		logRoute.DELETE("/", middleware.AdminAuth(), controller.DeleteHistoryLogs)
		logRoute.GET("/stat", middleware.AdminAuth(), controller.GetLogsStat)
		logRoute.GET("/self/stat", middleware.UserAuth(), controller.GetLogsSelfStat)
		logRoute.GET("/search", middleware.AdminAuth(), controller.SearchAllLogs)
		logRoute.GET("/self", middleware.UserAuth(), controller.GetUserLogs)
		logRoute.GET("/self/search", middleware.UserAuth(), controller.SearchUserLogs)
		groupRoute := apiRouter.Group("/group")
		groupRoute.Use(middleware.AdminAuth())
		{
			groupRoute.GET("/", controller.GetGroups)
		}
	}
}
