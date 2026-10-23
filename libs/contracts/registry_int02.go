package contracts

import "github.com/AlchemistBro/blood-supply-pet/libs/contracts/int02"

// ИНТ-02: прогноз ЧС от C02 (тело POST /int/forecast в planning)
func init() {
	registerInbound("EmergencyForecast", func() any { return &int02.EmergencyForecast{} })
}
