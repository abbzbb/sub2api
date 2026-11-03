package service

import (
	dbent "github.com/Wei-Shaw/sub2api/ent"
)

func ProvideRedeemService(redeemRepo RedeemCodeRepository, userRepo UserRepository, subscriptions *SubscriptionService, cache RedeemCache, billing *BillingCacheService, client *dbent.Client, auth APIKeyAuthCacheInvalidator, affiliate *AffiliateService, lottery *LotteryService) *RedeemService {
	s := NewRedeemService(redeemRepo, userRepo, subscriptions, cache, billing, client, auth, affiliate)
	s.lottery = lottery
	return s
}
