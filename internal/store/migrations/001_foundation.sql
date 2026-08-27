CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS buoys (
  id INTEGER PRIMARY KEY,
  buoy_no TEXT NOT NULL UNIQUE,
  device_type TEXT NOT NULL,
  last_communication_at TEXT NOT NULL,
  last_latitude REAL NOT NULL,
  last_longitude REAL NOT NULL,
  battery_basis_points INTEGER NOT NULL,
  lost_reason TEXT NOT NULL,
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS search_tasks (
  id INTEGER PRIMARY KEY,
  buoy_id INTEGER NOT NULL REFERENCES buoys(id),
  status TEXT NOT NULL,
  submitted_at TEXT,
  found_at TEXT,
  found_latitude REAL,
  found_longitude REAL,
  termination_reason TEXT,
  active_sector_set_version INTEGER,
  version INTEGER NOT NULL,
  event_sequence INTEGER NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS search_tasks_one_active_buoy
ON search_tasks(buoy_id)
WHERE status NOT IN ('found', 'terminated');

CREATE TABLE IF NOT EXISTS task_events (
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  sequence INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  actor_role TEXT NOT NULL,
  vessel_id INTEGER,
  occurred_at TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  PRIMARY KEY (task_id, sequence)
);

CREATE TRIGGER IF NOT EXISTS task_events_no_update
BEFORE UPDATE ON task_events
BEGIN
  SELECT RAISE(ABORT, 'task_events are append only');
END;

CREATE TRIGGER IF NOT EXISTS task_events_no_delete
BEFORE DELETE ON task_events
BEGIN
  SELECT RAISE(ABORT, 'task_events are append only');
END;

CREATE TABLE IF NOT EXISTS current_snapshots (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  effective_at TEXT NOT NULL,
  direction_millidegrees INTEGER NOT NULL,
  speed_milliknots INTEGER NOT NULL,
  uncertainty_millinautical_miles INTEGER NOT NULL,
  created_by TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TRIGGER IF NOT EXISTS current_snapshots_no_update
BEFORE UPDATE ON current_snapshots
BEGIN
  SELECT RAISE(ABORT, 'current_snapshots are immutable');
END;

CREATE TABLE IF NOT EXISTS sector_sets (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  version INTEGER NOT NULL,
  snapshot_id INTEGER NOT NULL REFERENCES current_snapshots(id),
  algorithm_version TEXT NOT NULL,
  normalized_input_json TEXT NOT NULL,
  input_digest TEXT NOT NULL,
  predicted_latitude REAL NOT NULL,
  predicted_longitude REAL NOT NULL,
  drift_distance_nm REAL NOT NULL,
  effective_radius_nm REAL NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(task_id, version)
);

CREATE TABLE IF NOT EXISTS search_sectors (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  sector_set_id INTEGER NOT NULL REFERENCES sector_sets(id),
  sector_set_version INTEGER NOT NULL,
  number INTEGER NOT NULL,
  priority INTEGER NOT NULL,
  name TEXT NOT NULL,
  polygon_json TEXT NOT NULL,
  area_square_nm REAL NOT NULL,
  centroid_latitude REAL NOT NULL,
  centroid_longitude REAL NOT NULL,
  coverage_basis_points INTEGER NOT NULL DEFAULT 0,
  claimed_status TEXT NOT NULL DEFAULT 'open',
  version INTEGER NOT NULL,
  UNIQUE(task_id, sector_set_version, number)
);

CREATE TABLE IF NOT EXISTS vessels (
  id INTEGER PRIMARY KEY,
  vessel_no TEXT NOT NULL UNIQUE,
  latitude REAL,
  longitude REAL,
  position_at TEXT,
  speed_milliknots INTEGER NOT NULL,
  endurance_seconds INTEGER NOT NULL,
  max_operation_millinautical_miles INTEGER NOT NULL,
  online_status TEXT NOT NULL,
  last_heartbeat_at TEXT,
  active_load INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedule_plans (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  sector_set_version INTEGER NOT NULL,
  plan_type TEXT NOT NULL,
  status TEXT NOT NULL,
  generated_at TEXT NOT NULL,
  expected_task_version INTEGER NOT NULL,
  decided_by TEXT,
  decision_reason TEXT,
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS assignments (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  plan_id INTEGER REFERENCES schedule_plans(id),
  sector_id INTEGER NOT NULL REFERENCES search_sectors(id),
  sector_number INTEGER NOT NULL,
  sector_set_version INTEGER NOT NULL,
  vessel_id INTEGER NOT NULL REFERENCES vessels(id),
  start_at TEXT NOT NULL,
  end_at TEXT NOT NULL,
  score INTEGER NOT NULL,
  status TEXT NOT NULL,
  source_assignment_id INTEGER REFERENCES assignments(id),
  claimed_by TEXT,
  claimed_at TEXT,
  actual_enter_at TEXT,
  actual_exit_at TEXT,
  exit_reason TEXT,
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS assignments_vessel_window_idx
ON assignments(vessel_id, start_at, end_at)
WHERE status IN ('confirmed', 'claimed', 'executing');

CREATE TABLE IF NOT EXISTS handoff_requests (
  id INTEGER PRIMARY KEY,
  source_assignment_id INTEGER NOT NULL REFERENCES assignments(id),
  target_vessel_id INTEGER REFERENCES vessels(id),
  reason TEXT NOT NULL,
  status TEXT NOT NULL,
  requested_by TEXT NOT NULL,
  confirmed_by TEXT,
  successor_assignment_id INTEGER REFERENCES assignments(id),
  effective_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS execution_reports (
  id INTEGER PRIMARY KEY,
  assignment_id INTEGER NOT NULL REFERENCES assignments(id),
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  vessel_id INTEGER NOT NULL REFERENCES vessels(id),
  report_type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS idempotency_records (
  task_id INTEGER NOT NULL,
  vessel_id INTEGER NOT NULL,
  operation TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_digest TEXT NOT NULL,
  response_status INTEGER NOT NULL,
  response_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(task_id, vessel_id, operation, idempotency_key)
);

CREATE TABLE IF NOT EXISTS replan_suggestions (
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES search_tasks(id),
  cause TEXT NOT NULL,
  vessel_id INTEGER,
  assignment_id INTEGER,
  from_sector_set_version INTEGER,
  to_sector_set_version INTEGER,
  dedupe_key TEXT NOT NULL,
  status TEXT NOT NULL,
  resolution_note TEXT,
  created_at TEXT NOT NULL,
  resolved_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS replan_suggestions_open_dedupe
ON replan_suggestions(dedupe_key)
WHERE status = 'open';

CREATE TABLE IF NOT EXISTS notifications (
  id INTEGER PRIMARY KEY,
  task_id INTEGER,
  recipient_role TEXT,
  recipient_id TEXT,
  type TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  title TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  read_at TEXT,
  resolved_at TEXT,
  created_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS notifications_open_dedupe
ON notifications(dedupe_key)
WHERE resolved_at IS NULL;
