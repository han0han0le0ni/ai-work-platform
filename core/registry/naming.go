package registry

import "regexp"

// 规范 3.7 命名规范。核心在注册点强制校验，避免脏名字进入注册表后
// 消费者永远找不到、订阅者永远收不到。
var (
	// 服务名：<名称>.<版本>，例如 llm.v1、toolExecutor.v1、com.example.echo.v1
	servicePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)*\.v[0-9]+$`)

	// 事件名：<命名空间>.<动作>，例如 chat.message、tool.before、core.log
	eventPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)*\.[A-Za-z][A-Za-z0-9]*$`)
)

// ValidServiceName 校验服务名是否带版本后缀。
func ValidServiceName(name string) bool {
	return servicePattern.MatchString(name)
}

// ValidEventName 校验事件名是否带命名空间。通配符 "*" 单独允许。
func ValidEventName(name string) bool {
	return name == Wildcard || eventPattern.MatchString(name)
}
