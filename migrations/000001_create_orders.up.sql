BEGIN;

CREATE TABLE orders (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,
    restaurant_id UUID NOT NULL,
    status TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    total_kopecks BIGINT NOT NULL,
    delivery_address TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT orders_status_valid CHECK (status IN ('created', 'cooking', 'ready', 'delivering', 'delivered', 'cancelled')),
    CONSTRAINT orders_version_positive CHECK (version > 0),
    CONSTRAINT orders_total_positive CHECK (total_kopecks > 0),
    CONSTRAINT orders_address_length CHECK (char_length(delivery_address) BETWEEN 1 AND 300),
    CONSTRAINT orders_address_not_blank CHECK (delivery_address !~ '^[[:space:]]*$'),
    CONSTRAINT orders_timestamps_valid CHECK (updated_at >= created_at)
);

CREATE TABLE order_items (
    order_id UUID NOT NULL REFERENCES orders(id),
    menu_item_id UUID NOT NULL,
    name_snapshot TEXT NOT NULL,
    unit_price_kopecks BIGINT NOT NULL,
    quantity INTEGER NOT NULL,
    PRIMARY KEY (order_id, menu_item_id),
    CONSTRAINT order_items_name_not_blank CHECK (name_snapshot !~ '^[[:space:]]*$'),
    CONSTRAINT order_items_price_range CHECK (unit_price_kopecks BETWEEN 1 AND 1000000),
    CONSTRAINT order_items_quantity_range CHECK (quantity BETWEEN 1 AND 20)
);

CREATE TABLE idempotency_keys (
    user_id UUID NOT NULL,
    key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    order_id UUID NOT NULL REFERENCES orders(id),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, key),
    CONSTRAINT idempotency_keys_key_length CHECK (char_length(key) BETWEEN 1 AND 128),
    CONSTRAINT idempotency_keys_key_not_blank CHECK (key !~ '^[[:space:]]*$'),
    CONSTRAINT idempotency_keys_hash_format CHECK (request_hash ~ '^[0-9a-f]{64}$')
);

CREATE TABLE outbox_events (
    event_id UUID PRIMARY KEY,
    order_id UUID NOT NULL REFERENCES orders(id),
    type TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT outbox_events_type_not_blank CHECK (type !~ '^[[:space:]]*$'),
    CONSTRAINT outbox_events_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT outbox_events_attempts_nonnegative CHECK (attempts >= 0)
);

CREATE INDEX outbox_events_pending_idx
    ON outbox_events (next_attempt_at, created_at)
    WHERE published_at IS NULL;

COMMIT;
