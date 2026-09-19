-- Dialect Corpus Platform - Listening Experiment Schema (002)
-- This file is for reference; actual migrations run via internal/database/database.go

-- 听辨实验：按调查点挑条目、打乱编排、分发给听辨人
CREATE TABLE IF NOT EXISTS listening_experiments (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(200) NOT NULL,
    dialect_point_code VARCHAR(50) NOT NULL,
    wordlist_id BIGINT NOT NULL DEFAULT 0,                 -- 0 = 不限词表
    entry_ids BIGINT[] NOT NULL DEFAULT '{}',             -- 选中条目（规范序，只增不减）
    listener_ids BIGINT[] NOT NULL DEFAULT '{}',
    status VARCHAR(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','completed','archived')),
    seed BIGINT NOT NULL,
    current_position INT NOT NULL DEFAULT 0,              -- 断点续发游标
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 试次：某个条目在该调查点下的一段实际音频；position 全局递增，作废不移位
CREATE TABLE IF NOT EXISTS listening_trials (
    id BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES listening_experiments(id) ON DELETE CASCADE,
    entry_id BIGINT NOT NULL,
    segment_id BIGINT NOT NULL DEFAULT 0,
    object_key VARCHAR(500) NOT NULL DEFAULT '',
    position INT NOT NULL,
    batch_no INT NOT NULL DEFAULT 1,
    status VARCHAR(20) NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','voided')),
    void_reason TEXT DEFAULT '',
    voided_at TIMESTAMPTZ,
    replacement_for BIGINT NOT NULL DEFAULT 0,            -- 补的是哪条作废 trial
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_listening_trials_position
    ON listening_trials(experiment_id, position);
CREATE INDEX IF NOT EXISTS idx_listening_trials_experiment
    ON listening_trials(experiment_id, status);

-- 分发：trial × listener，order_index 为该听辨人自己的收听次序（轮换后各不相同）
CREATE TABLE IF NOT EXISTS listening_assignments (
    id BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES listening_experiments(id) ON DELETE CASCADE,
    listener_id BIGINT NOT NULL REFERENCES speakers(id),
    trial_id BIGINT NOT NULL REFERENCES listening_trials(id) ON DELETE CASCADE,
    order_index INT NOT NULL,
    "offset" INT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','answered','voided')),
    issued_at TIMESTAMPTZ,
    answered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_listening_assignments_listener_trial
    ON listening_assignments(listener_id, trial_id);
CREATE INDEX IF NOT EXISTS idx_listening_assignments_exp_listener
    ON listening_assignments(experiment_id, listener_id, status);
CREATE INDEX IF NOT EXISTS idx_listening_assignments_trial
    ON listening_assignments(trial_id);

-- 作答：(listener_id, trial_id) 唯一 —— 同一人对同一条只留第一次
CREATE TABLE IF NOT EXISTS listening_responses (
    id BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES listening_experiments(id) ON DELETE CASCADE,
    assignment_id BIGINT NOT NULL REFERENCES listening_assignments(id) ON DELETE CASCADE,
    listener_id BIGINT NOT NULL REFERENCES speakers(id),
    trial_id BIGINT NOT NULL REFERENCES listening_trials(id) ON DELETE CASCADE,
    entry_id BIGINT NOT NULL,
    answer TEXT NOT NULL,
    confidence INT NOT NULL DEFAULT 0,
    note TEXT DEFAULT '',
    client_token VARCHAR(200) DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_listening_responses_listener_trial
    ON listening_responses(listener_id, trial_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_listening_responses_token
    ON listening_responses(experiment_id, client_token) WHERE client_token <> '';
CREATE INDEX IF NOT EXISTS idx_listening_responses_experiment
    ON listening_responses(experiment_id);
