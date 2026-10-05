BEGIN;

DROP TABLE outbox_events;
DROP TABLE idempotency_keys;
DROP TABLE order_items;
DROP TABLE orders;

COMMIT;
