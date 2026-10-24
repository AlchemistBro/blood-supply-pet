// Package auth — проверка JWT (OIDC, Keycloak) и матрица «роль → операция» (NFR011),
// единая для всех сервисов (docs/architecture/security.md).
package auth

// Role — роль пользователя в Keycloak.
type Role string

// Роли системы. SERVICE — служебный токен для внутренних вызовов (ADR-0004).
const (
	RolePhysician Role = "PHYSICIAN"
	RoleOperator  Role = "CLINIC_OPERATOR"
	RoleAnalyst   Role = "TERRITORIAL_ANALYST"
	RoleDuty      Role = "DUTY_OFFICER"
	RoleITService Role = "IT_SERVICE"
	RoleService   Role = "SERVICE"
)

// Operation — операция, на которую проверяются права.
type Operation string

// Операции. Список расширяется по этапам; каждая новая операция добавляется сюда и в матрицу.
const (
	OpViewStock          Operation = "VIEW_STOCK"
	OpEnterConsumption   Operation = "ENTER_ANNUAL_CONSUMPTION"
	OpSyncOnDemand       Operation = "SYNC_ON_DEMAND"
	OpUploadStatistics   Operation = "UPLOAD_STATISTICS"
	OpPublishEstimate    Operation = "PUBLISH_ESTIMATE"
	OpViewDemand         Operation = "VIEW_DEMAND"
	OpViewPlan           Operation = "VIEW_PLAN"
	OpBuildPlan          Operation = "BUILD_PLAN"
	OpAdjustForecast     Operation = "ADJUST_FORECAST"
	OpViewTasks          Operation = "VIEW_TASKS"
	OpOpenObligation     Operation = "OPEN_OBLIGATION"
	OpCancelObligation   Operation = "CANCEL_OBLIGATION"
	OpViewObligation     Operation = "VIEW_OBLIGATION"
	OpConfirmTransfer    Operation = "CONFIRM_TRANSFER"
	OpReplaceUnit        Operation = "REPLACE_UNIT"
	OpRejectTransfer     Operation = "REJECT_TRANSFER"
	OpDispatchTransfer   Operation = "DISPATCH_TRANSFER"
	OpReceiveTransfer    Operation = "RECEIVE_TRANSFER"
	OpCreateRequest      Operation = "CREATE_REQUEST"
	OpViewRequests       Operation = "VIEW_REQUESTS"
	OpSelectVariant      Operation = "SELECT_VARIANT"
	OpCancelRequest      Operation = "CANCEL_REQUEST"
	OpReadPatientCase    Operation = "READ_PATIENT_CASE"
	OpHandleEscalation   Operation = "HANDLE_ESCALATION"
	OpManageRules        Operation = "MANAGE_RULES"
	OpViewRules          Operation = "VIEW_RULES"
	OpReadAudit          Operation = "READ_AUDIT"
	OpWriteAudit         Operation = "WRITE_AUDIT"
	OpInternalUnitStatus Operation = "INTERNAL_UNIT_STATUS"
	OpReceiveForecast    Operation = "RECEIVE_FORECAST"
	OpDevPanel           Operation = "DEV_PANEL"
	OpViewIntegrations   Operation = "VIEW_INTEGRATIONS"
	OpTraceUnits         Operation = "TRACE_UNITS"
)

// policy — матрица: операция → роли, которым она разрешена.
// Обязательство O07 (этап 1, FR048, FR049): открывают matching и planning служебным токеном, отменяет
// служба или дежурный, смотрят участники передачи.
var policy = map[Operation][]Role{
	OpViewStock:          {RolePhysician, RoleOperator, RoleDuty, RoleITService},
	OpEnterConsumption:   {RoleOperator},
	OpSyncOnDemand:       {RoleOperator},
	OpUploadStatistics:   {RoleAnalyst},
	OpPublishEstimate:    {RoleAnalyst},
	OpViewDemand:         {RoleAnalyst, RoleDuty},
	OpViewPlan:           {RoleOperator, RoleAnalyst, RoleDuty},
	OpBuildPlan:          {RoleAnalyst, RoleDuty},
	OpAdjustForecast:     {RoleOperator},
	OpViewTasks:          {RoleOperator, RoleDuty},
	OpOpenObligation:     {RoleService},
	OpCancelObligation:   {RoleService, RoleDuty},
	OpViewObligation:     {RoleOperator, RolePhysician, RoleDuty, RoleService, RoleITService},
	OpConfirmTransfer:    {RoleOperator, RoleDuty},
	OpReplaceUnit:        {RoleOperator, RoleDuty},
	OpRejectTransfer:     {RoleOperator, RoleDuty},
	OpDispatchTransfer:   {RoleOperator, RoleDuty},
	OpReceiveTransfer:    {RoleOperator, RoleDuty},
	OpCreateRequest:      {RolePhysician},
	OpViewRequests:       {RolePhysician, RoleDuty},
	OpSelectVariant:      {RolePhysician},
	OpCancelRequest:      {RolePhysician},
	OpReadPatientCase:    {RolePhysician, RoleOperator},
	OpHandleEscalation:   {RoleDuty},
	OpManageRules:        {RoleITService},
	OpViewRules:          {RolePhysician, RoleITService},
	OpReadAudit:          {RoleDuty, RoleITService},
	OpWriteAudit:         {RoleService},
	OpInternalUnitStatus: {RoleService},
	OpReceiveForecast:    {RoleService},
	OpDevPanel:           {RolePhysician, RoleOperator, RoleAnalyst, RoleDuty, RoleITService},
	OpViewIntegrations:   {RoleITService, RoleDuty},
	OpTraceUnits:         {RoleOperator, RoleDuty},
}

// Allowed сообщает, разрешена ли операция хотя бы одной из ролей.
func Allowed(op Operation, roles []Role) bool {
	allowed, ok := policy[op]
	if !ok {
		return false
	}
	for _, r := range roles {
		for _, a := range allowed {
			if r == a {
				return true
			}
		}
	}
	return false
}

// Operations возвращает все известные операции (для тестов полноты матрицы).
func Operations() []Operation {
	out := make([]Operation, 0, len(policy))
	for op := range policy {
		out = append(out, op)
	}
	return out
}

// RolesFor возвращает роли, которым разрешена операция.
func RolesFor(op Operation) []Role {
	return append([]Role(nil), policy[op]...)
}
