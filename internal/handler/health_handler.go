package handler

import (
	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthHandler handles liveness and readiness probe endpoints.
type HealthHandler struct {
	db *pgxpool.Pool
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(db *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{db: db}
}

// healthData is the payload returned by both health endpoints.
type healthData struct {
	Status string `json:"status"`
}

// Live godoc
//
//	@Summary		Liveness check
//	@Description	Returns 200 when the HTTP server is running.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	response.successEnvelope{data=healthData}
//	@Router			/health [get]
func (h *HealthHandler) Live(c *gin.Context) {
	response.OK(c, healthData{Status: "ok"})
}

// Ready godoc
//
//	@Summary		Readiness check
//	@Description	Returns 200 when the server can accept traffic (database reachable).
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	response.successEnvelope{data=healthData}
//	@Failure		503	{object}	response.errorEnvelope
//	@Router			/health/ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	if h.db == nil {
		response.InternalServerError(c)
		return
	}

	if err := database.Ping(c.Request.Context(), h.db); err != nil {
		// 503 is not in our standard set, so we write it manually here only.
		// A proper ServiceUnavailable helper can be added if more endpoints need it.
		c.JSON(503, gin.H{
			"success": false,
			"error": gin.H{
				"code":    "DATABASE_UNAVAILABLE",
				"message": "Database is not reachable",
			},
			"meta": gin.H{
				"request_id": c.GetString("request_id"),
			},
		})
		return
	}

	response.OK(c, healthData{Status: "ok"})
}
