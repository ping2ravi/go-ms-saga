package ms

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	ginadapter "github.com/awslabs/aws-lambda-go-api-proxy/gin"
	"github.com/gin-gonic/gin"
	"github.com/ping2ravi/go-ms-saga/ms/db"
	"gorm.io/gorm"
)

var ginLambdaV2 *ginadapter.GinLambdaV2

func IsAwsLambdaEnv() bool {
	lambdaTaskRoot := os.Getenv("LAMBDA_TASK_ROOT")
	return lambdaTaskRoot != ""
}

var ginEngine *gin.Engine
var gormDb *gorm.DB
var apiErrorHandler ApiErrorHandler

func InitV2(routes []Route, apiErrorHandlerParam ApiErrorHandler, gormDbParam *gorm.DB) {
	gormDb = gormDbParam
	apiErrorHandler = apiErrorHandlerParam
	start := time.Now()
	log.Printf("Gin cold start %v", start)
	ginEngine = gin.New()
	LoadAllRoutes(ginEngine, routes)

	if IsAwsLambdaEnv() {
		log.Printf("Gin Lambda V2")
		ginLambdaV2 = ginadapter.NewV2(ginEngine)
	}
	log.Printf("Gin Started")

}

func MainV2() {
	if IsAwsLambdaEnv() {
		lambda.Start(HandlerV2)
	} else {
		if err := ginEngine.Run(); err != nil {
			log.Printf("error starting server %+v", err)
		}
	}
}

// Handler is our lambda handler invoked by the `lambda.Start` function call
func HandlerV2(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {

	awsResponse, err := ginLambdaV2.ProxyWithContext(ctx, request)

	return awsResponse, err
}

func LoadAllRoutes(r *gin.Engine, routes []Route) {

	for _, oneRoute := range routes {
		if oneRoute.Method == "GET" {
			r.GET(oneRoute.Path, wrapper(oneRoute.Handler))

		} else if oneRoute.Method == "POST" {
			r.POST(oneRoute.Path, wrapper(oneRoute.Handler))
		} else if oneRoute.Method == "PUT" {
			r.PUT(oneRoute.Path, wrapper(oneRoute.Handler))
		} else if oneRoute.Method == "OPTIONS" {
			r.OPTIONS(oneRoute.Path, wrapper(oneRoute.Handler))
		} else {
			log.Printf("UNKNOWN PATH CONFIG %+v", oneRoute)
		}
	}

	log.Printf("Loaded All Routes")

}

func wrapper(actual func(context *gin.Context) interface{}) func(context *gin.Context) {

	return func(context *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered in f , error is %v", r)
				log.Println("stacktrace from panic: \n" + string(debug.Stack()))
				apiRequestKey, _, _ := generateApiRequestKey(context)
				updateApiReuestEndRecord(context, apiRequestKey, "Fail")
				apiErrorHandler(context, r)
				// error, ok := r.(ApiError)
				// if ok {
				// 	if error.Code >= 500 && error.Code <= 599 {
				// 		context.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"status": false, "message": "Internal Server Error"})
				// 	} else if error.Code == 400 {
				// 		context.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"status": false, "message": error.Message})
				// 	} else if error.Code == 401 {
				// 		context.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"status": false, "message": error.Message})
				// 	} else if error.Code == 402 {
				// 		context.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"status": false, "message": error.Message})
				// 	} else if error.Code == 403 {
				// 		context.AbortWithStatusJSON(http.StatusForbidden, gin.H{"status": false, "message": error.Message})
				// 	} else if error.Code == 404 {
				// 		context.AbortWithStatusJSON(http.StatusNotFound, gin.H{"status": false, "message": error.Message})
				// 	} else if error.Code >= 300 && error.Code <= 399 {
				// 		context.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"status": false, "message": error.Message})
				// 	}
				// } else {
				// 	context.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"status": false, "message": r})
				// }
				return
			} else {
				log.Printf("No error found")
			}
		}()
		apiRequestKey, shouldReturn := newFunction(context)
		if shouldReturn {
			return
		}
		response := actual(context)
		context.JSON(http.StatusOK, response)
		//Ignore error for now
		updateApiReuestEndRecord(context, apiRequestKey, "Success")

	}

}

func newFunction(context *gin.Context) (string, bool) {
	resourcePath := context.Request.URL.Path

	log.Printf("resourcePath : %v\n", resourcePath)
	log.Printf("resourceMethod : %v\n", context.Request.Method)

	apiRequestKey, businessTxnId, err := generateApiRequestKey(context)
	if err != nil {
		context.JSON(http.StatusConflict, "{'message': '"+err.Error()+"'}")
		return "", true
	}
	err = checkIfApiHasAlreadyBeenCalled(context, apiRequestKey)
	if err != nil {
		context.JSON(http.StatusConflict, "{'message': '"+err.Error()+"'}")
		return "", true
	}
	err = createApiReuestStartRecord(context, apiRequestKey, businessTxnId)
	if err != nil {
		context.JSON(http.StatusConflict, "{'message': '"+err.Error()+"'}")
		return "", true
	}
	return apiRequestKey, false
}
func checkIfApiHasAlreadyBeenCalled(ginContext *gin.Context, apiRequestKey string) error {

	var apiRequest db.ApiRequest
	gormDb.Where(&db.ApiRequest{ApiRequestKey: apiRequestKey}).Find(&apiRequest)
	if apiRequest.ApiRequestKey != "" {
		return errors.New("request has already been processed")
	}
	return nil

}

func generateApiRequestKey(ginContext *gin.Context) (string, string, error) {
	resourcePath := ginContext.Request.URL.Path

	txnId := ginContext.Request.Header.Get("s-txn-id")
	if txnId == "" {
		return "", "", errors.New("no s-txn-id header found ")
	}
	businessTxnId := ginContext.Request.Header.Get("s-bus-txn-id")
	if txnId == "" {
		return "", "", errors.New("no s-bus-txn-id header found ")
	}

	apiRequestKey := getSha(resourcePath) + txnId + businessTxnId
	return apiRequestKey, businessTxnId, nil
}
func createApiReuestStartRecord(ginContext *gin.Context, apiRequestKey string, businessTxnId string) error {

	startTime := time.Now()
	apiRequest := db.ApiRequest{
		BusinessTxnId: businessTxnId,
		ApiRequestKey: apiRequestKey,
		Ver:           0,
		ApiUrl:        ginContext.Request.URL.Path,
		Status:        "Start",
		StartTime:     &startTime,
	}
	response := gormDb.Create(&apiRequest)
	if response.Error != gorm.ErrNotImplemented {
		return response.Error
	}
	return nil

}
func updateApiReuestEndRecord(ginContext *gin.Context, apiRequestKey string, status string) error {

	var apiRequest db.ApiRequest
	gormDb.Where(&db.ApiRequest{ApiRequestKey: apiRequestKey}).Find(&apiRequest)
	if apiRequest.ApiRequestKey == "" {
		return errors.New("request has already been processed")
	}
	endTime := time.Now()
	apiRequest.Status = status
	apiRequest.EndTime = &endTime

	response := gormDb.Save(&apiRequest)
	if response.Error != gorm.ErrNotImplemented {
		return response.Error
	}
	return nil

}

func getSha(text string) string {
	h := sha1.New()
	h.Write([]byte(text))
	sha1_hash := hex.EncodeToString(h.Sum(nil))
	return sha1_hash
}

type Route struct {
	Path    string
	Method  string
	Handler HandlerFuncWithToken
}
type HandlerFuncWithToken func(*gin.Context) interface{}

type ApiError struct {
	Code    int32
	Message string
	Source  string
}

type ApiErrorHandler func(*gin.Context, interface{})
