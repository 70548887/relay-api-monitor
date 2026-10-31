package backups

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"

	"github.com/aws/smithy-go"
	"relaylab/internal/upstream"
)

// StorageError is intentionally detached from upstream error text. Only these
// controlled fields may enter an API response, health record, audit or log.
type StorageError struct {
	Code       string
	Stage      string
	Message    string
	HTTPStatus int
}

func (e *StorageError) Error() string { return e.Message }

func storageWritePreconditionFailed(cause error) bool {
	var status interface{ HTTPStatusCode() int }
	if errors.As(cause, &status) && status.HTTPStatusCode() == 412 {
		return true
	}
	var service smithy.APIError
	return errors.As(cause, &service) && strings.EqualFold(service.ErrorCode(), "PreconditionFailed")
}

func storageFailure(ctx context.Context, stage string, cause error) *StorageError {
	failure := &StorageError{Code: "storage_" + stage + "_failed", Stage: stage, Message: "S3 存储操作失败，请重试或检查配置"}
	var previous *StorageError
	if errors.As(cause, &previous) {
		copy := *previous
		return &copy
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(cause, &status) {
		failure.HTTPStatus = status.HTTPStatusCode()
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(cause, context.Canceled) {
		failure.Code, failure.Message = "canceled", "S3 连接测试已取消，正在核对测试对象清理结果"
		return failure
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		failure.Code, failure.Message = "timeout", "S3 操作超过等待时限，请检查网络后重试"
		return failure
	}
	var outbound *upstream.Error
	if errors.As(cause, &outbound) {
		switch outbound.Code {
		case "dns_error":
			failure.Code, failure.Stage, failure.Message = "dns_error", "dns", "S3 域名解析失败，请检查 DNS 配置"
		case "fake_ip_blocked":
			failure.Code, failure.Stage, failure.Message = "fake_ip_blocked", "dns", "S3 域名被解析为代理虚拟地址；请配置安全 DNS 解析或排除该域名的 Fake-IP"
		case "blocked_target", "protected_target":
			failure.Code, failure.Stage, failure.Message = "blocked_target", "dns", "S3 连接地址被安全出口策略拒绝，请检查端点与 DNS 配置"
		case "connection_timeout":
			failure.Code, failure.Stage, failure.Message = "connection_timeout", "connecting", "连接 S3 服务超时，请检查网络路径"
		case "connection_error", "network_error":
			failure.Code, failure.Stage, failure.Message = "connection_error", "connecting", "无法连接 S3 服务，请检查网络路径"
		case "timeout":
			failure.Code, failure.Message = "timeout", "S3 操作超过等待时限，请检查网络后重试"
		case "canceled":
			failure.Code, failure.Message = "canceled", "S3 连接测试已取消，正在核对测试对象清理结果"
		default:
			failure.Code, failure.Stage, failure.Message = "connection_error", "connecting", "无法连接 S3 服务，请检查网络路径"
		}
		return failure
	}
	var dns *net.DNSError
	if errors.As(cause, &dns) {
		failure.Code, failure.Stage, failure.Message = "dns_error", "dns", "S3 域名解析失败，请检查 DNS 配置"
		return failure
	}
	var certificate *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostMismatch x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(cause, &certificate) || errors.As(cause, &unknownAuthority) || errors.As(cause, &hostMismatch) || errors.As(cause, &invalidCertificate) || errors.As(cause, &recordHeader) {
		failure.Code, failure.Stage, failure.Message = "tls_error", "connecting", "S3 TLS 连接或证书校验失败，请检查端点和证书"
		return failure
	}
	var network net.Error
	if errors.As(cause, &network) && network.Timeout() {
		failure.Code, failure.Message = "timeout", "S3 网络操作超时，请检查连接后重试"
		return failure
	}
	var operation *net.OpError
	if errors.As(cause, &operation) {
		failure.Code, failure.Stage, failure.Message = "connection_error", "connecting", "无法连接 S3 服务，请检查网络路径"
		return failure
	}
	apiCode := ""
	var service smithy.APIError
	if errors.As(cause, &service) {
		apiCode = strings.ToLower(service.ErrorCode())
	}
	switch {
	case apiCode == "preconditionfailed" || failure.HTTPStatus == 412:
		failure.Code, failure.Message = "object_conflict", "S3 已存在同名对象，本次测试未覆盖或删除它，请重新测试"
	case apiCode == "invalidaccesskeyid" || apiCode == "signaturedoesnotmatch" || apiCode == "invalidtoken" || apiCode == "expiredtoken" || failure.HTTPStatus == 401:
		failure.Code, failure.Message = "authentication_failed", "S3 身份验证失败，请核对 Access Key、Secret 和区域"
	case apiCode == "accessdenied" || failure.HTTPStatus == 403:
		failure.Code, failure.Message = "permission_denied", "S3 拒绝此阶段的操作，请检查存储桶及对象权限"
	case apiCode == "nosuchbucket" || failure.HTTPStatus == 404 && stage == "writing":
		failure.Code, failure.Message = "bucket_not_found", "S3 存储桶不存在或当前凭据无法访问，请核对桶名"
	case apiCode == "nosuchkey" || failure.HTTPStatus == 404:
		failure.Code, failure.Message = "object_not_found", "S3 测试对象不可见，请检查读取权限和存储一致性"
	case failure.HTTPStatus == 429 || failure.HTTPStatus >= 500:
		failure.Code, failure.Message = "service_unavailable", "S3 服务暂时不可用或请求被限流，请稍后重试"
	default:
		switch stage {
		case "writing":
			failure.Code, failure.Message = "write_failed", "S3 测试对象写入失败，请检查连接与写入权限"
		case "reading":
			failure.Code, failure.Message = "read_failed", "S3 测试对象读取失败，请检查连接与读取权限"
		case "cleaning":
			failure.Code, failure.Message = "cleanup_failed", "S3 测试对象删除未获确认，可能有测试对象残留"
		}
	}
	return failure
}
