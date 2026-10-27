-- Down 913: drop monthly bills / coupons / invoice placeholder.
DROP POLICY IF EXISTS tenant_isolation_maas_invoice_requests ON maas_invoice_requests;
DROP POLICY IF EXISTS tenant_isolation_maas_coupon_redemptions ON maas_coupon_redemptions;
DROP POLICY IF EXISTS tenant_isolation_maas_monthly_bills ON maas_monthly_bills;

DROP TABLE IF EXISTS maas_invoice_requests;
DROP TABLE IF EXISTS maas_coupon_redemptions;
DROP TABLE IF EXISTS maas_coupons;
DROP TABLE IF EXISTS maas_monthly_bills;
