package httpx

import "github.com/gin-gonic/gin"

const ownerKey = "authenticatedOwner"

func Owner(c *gin.Context) string { return c.GetString(ownerKey) }

// SetOwner is called only by authentication middleware after verifying the token.
func SetOwner(c *gin.Context, owner string) { c.Set(ownerKey, owner) }
