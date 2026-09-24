package leads

import (
	"testing"

	"github.com/DenisHumen/krokosha-site/api/internal/config"
	"github.com/DenisHumen/krokosha-site/api/internal/loyalty"
)

func TestDiscountWords(t *testing.T) {
	rules := config.Loyalty{Enabled: true, Tiers: []config.LoyaltyTier{{ID: "silver", Name: config.Localized{"en": "Silver", "uk": "Срібний", "ru": "Серебряный"}, Orders: 1, Discount: 5}}}
	for _, tc := range []struct {
		offer loyalty.Offer
		lang  string
		owner bool
		want  string
	}{
		{loyalty.Offer{}, "ru", true, ""},
		{loyalty.Offer{Percent: 10, Reason: loyalty.ReasonWelcome}, "ru", false, "10% — первая заявка"},
		{loyalty.Offer{Percent: 10, Reason: loyalty.ReasonWelcome}, "de", false, "10% — first request"},
		{loyalty.Offer{Percent: 5, Reason: loyalty.ReasonTier, Detail: "silver"}, "uk", false, "5% — рівень «Срібний»"},
		{loyalty.Offer{Percent: 5, Reason: loyalty.ReasonTier, Detail: "silver"}, "en", false, "5% — Silver level"},
		{loyalty.Offer{Percent: 20, Reason: loyalty.ReasonEggs}, "en", false, "20% — every easter egg found"},
		{loyalty.Offer{Percent: 30, Reason: loyalty.ReasonPersonal, Detail: "партнёр"}, "ru", false, "30% — персональная (партнёр)"},
		{loyalty.Offer{Percent: 25, Reason: loyalty.ReasonManual, Detail: "за отзыв"}, "ru", true, "25% — вручную (за отзыв)"},
		{loyalty.Offer{Percent: 25, Reason: loyalty.ReasonManual, Detail: "за отзыв"}, "ru", false, "25% — за отзыв"},
		{loyalty.Offer{Percent: 25, Reason: loyalty.ReasonManual}, "ru", false, "25%"},
	} {
		if got := DiscountWords(rules, tc.offer, tc.lang, tc.owner); got != tc.want {
			t.Errorf("%+v %s owner=%v: %q, want %q", tc.offer, tc.lang, tc.owner, got, tc.want)
		}
	}
}
