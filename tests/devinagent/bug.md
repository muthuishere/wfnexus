# Bug: cart total is wrong when a coupon is applied twice

Steps: add item ($100), apply coupon SAVE10, apply SAVE10 again.
Expected: $90. Actual: $81.

Relevant code: apps/api/internal/engine/pricing.go — applyCoupon() appends to
order.Discounts without checking whether the coupon code is already present,
and total() folds every entry in the slice.
