# 近海失联浮标漂移搜寻闭环服务

## 项目目标

建设一个面向近海巡检队的 Go HTTP 后端，以关系型 SQLite 数据库持久化浮标、任务、预测版本、扇区、船只、分配、事件和通知。系统通过不可变预测版本、乐观并发控制、事务性领取、幂等执行上报和可恢复的离线巡检形成完整搜寻闭环。默认采用 UTC 与 WGS84，使用可注入时钟和编号生成器保证公开测试不依赖休眠或随机值。仓库包含 go.mod、数据库迁移、单服务 Dockerfile，并通过构建参数支持 linux/amd64 与 linux/arm64。生产实现规划为约 25 个 Go 文件、2500 行有效代码，控制在 2000—3000 行内，测试代码不计入该预算。

## 端到端业务流程

1. 任务指挥员登记或更新失联浮标，记录最后通信时间、坐标、电量及失联原因；创建草稿任务并提交一个或多个具有生效时间的海流预测快照，提交任务后进入待调度状态。
2. 指挥员选择预测快照生成不可变的扇区版本；系统保存推演输入、算法版本、输入摘要和排序稳定的多边形结果，并基于在线船只生成自动分配方案。
3. 指挥员审阅方案，可驳回、调整船只与执行窗口或确认方案；首次确认有效方案时任务进入搜寻中，确认事务同时校验船只续航、作业距离、时间重叠和客户端版本。
4. 船队操作员领取已确认扇区，上报船位、进入时间、覆盖增量、疑似发现和退出原因；扇区交接通过交接请求与后继分配完成，覆盖进度保留在扇区上而不因更换船只清零。
5. 心跳巡检发现船只离线、剩余续航无法完成任务，或新预测产生更新扇区版本时，系统创建去重的重调度建议和站内通知；旧版执行记录继续保留，由指挥员决定取消、交接或完成。
6. 指挥员依据疑似发现确认浮标已找到，或填写原因终止任务；审计员随后筛选只追加时间线、查看覆盖率和船只利用率，并导出包含预测、调度、执行、异常与结论的复盘摘要。

## 核心组件与职责

1. HTTP 接入与角色控制：提供 JSON API、参数校验、统一错误、分页及任务详情聚合；通过 X-Actor-ID 与 X-Role 标识任务指挥员、船队操作员和审计员，并执行固定权限矩阵。
2. 浮标与任务控制：维护浮标档案、单浮标活动任务约束、任务状态机、终止或发现结论以及任务级乐观版本。
3. 漂移推演与版本管理：管理带生效时间的海流快照，执行确定性漂移计算，生成不可变扇区集合并识别执行版本过期。
4. 船队与调度：维护船只能力、位置、心跳和在线状态，计算候选船只与预计窗口，管理自动方案、人工调整、确认及冲突检测。
5. 执行协同与重调度：处理扇区领取、位置回报、进入、覆盖进度、疑似发现、退出和交接，并根据离线、续航或预测变化产生重调度建议。
6. 审计、通知与持久恢复：维护只追加时间线、告警通知、统计与复盘导出；封装迁移、事务、幂等记录、启动恢复和周期巡检。

## 领域规则与不变量

1. 角色权限固定：任务指挥员可维护浮标、任务、预测和调度决策；船队操作员可维护船只运行数据并执行已确认分配；审计员只可读取任务、时间线、统计和复盘。通知只能由其接收者或指挥员标记已读或处理。
2. 浮标编号全局唯一；坐标必须满足纬度 [-90,90]、经度 [-180,180]，生产数据存为 WGS84；所有时间接收带时区的 RFC3339，转换为 UTC 存储和输出。
3. 任务状态为草稿、待调度、搜寻中、暂停、已找到、已终止。允许草稿→待调度或已终止，待调度→搜寻中或已终止，搜寻中→暂停、已找到或已终止，暂停→搜寻中、已找到或已终止；终态不可离开。终止必须提供原因，确认找到必须提供时间和坐标。
4. 同一浮标至多有一个非已找到且非已终止任务，由数据库部分唯一索引和服务校验共同保证。任务、船只、扇区与分配均持有递增版本；修改请求必须提交 expected_version，版本不符返回 409 stale_version。
5. 非法状态转换返回 409 invalid_transition，并在独立审计事务中追加包含请求者、原状态、目标状态和拒绝原因的 transition_rejected 事件，不得改变任务状态或版本。
6. 海流快照一经创建不可修改，包含生效时间、流向角、速度、不确定半径和创建者。流向表示水流指向的真方位角，速度和半径分别以节和海里输入并按千分之一单位持久化；生成请求必须明确引用快照，避免由当前时间隐式选择。
7. 漂移算法 fan-v1 使用快照生效时间与浮标最后通信时间之差作为失联时长 t；漂移距离 d=流速×t，东向分量为 d×sin(流向)，北向分量为 d×cos(流向)，通过每纬度 60 海里及原纬度余弦换算预测中心。有效半径 R=不确定半径+0.1×d，输入纬度限制在 [-85,85]，最终坐标按十进制度六位使用四舍五入固定化。
8. 每次推演围绕预测中心生成四个互不覆盖内部区域的 90 度扇形多边形，边界每 15 度取点并闭合为 GeoJSON 环。顺流、顺时针侧向、逆时针侧向、逆流扇区的优先级依次为 1、2、3、4；扇区编号按该顺序生成，面积和质心使用相同算法版本计算并固定精度。
9. 每次推演创建递增的 sector_set_version，保存规范化输入、SHA-256 输入摘要、算法版本及完整结果。新版本不能更新或删除旧扇区、分配和进度；若任务已有执行版本，新版本立即产生 prediction_changed 重调度建议。
10. 船只保存最新位置、巡航航速、剩余续航小时、最大作业距离、在线状态、最后心跳和当前负载。默认心跳超时为五分钟；巡检仅在 last_heartbeat≤检查时刻减去阈值时通过条件更新标记离线，避免与新心跳竞争。
11. 自动调度只考虑在线且能力数据完整的船只。到扇区距离使用 WGS84 半正矢公式；ETA=距离/巡航速度，预计作业时长=扇区面积/(巡航速度×0.5 海里有效扫宽)。要求往返距离不超过最大作业距离，且 2×ETA+作业时长不超过剩余续航。
12. 调度按扇区优先级和编号依次处理。候选整数评分为 ETA 秒数+当前有效负载×1800+ceil(3600×所需续航秒数/剩余续航秒数)，低分优先，同分按船只编号升序；方案生成时即时预留窗口和扣减模拟续航，因此同一方案内的后续分配也参与冲突判断。
13. 分配时间区间采用左闭右开 [start_at,end_at)，首尾相接不算冲突。确认自动或人工方案时，在同一写事务中校验方案版本、任务版本以及该船只全部已确认、已领取或执行中的分配；任一重叠都拒绝整个确认并生成 assignment_conflict 通知和审计事件。
14. 首次确认待调度任务的有效方案时原子地转为搜寻中；搜寻中和暂停任务可确认重调度方案，但暂停任务的分配在恢复搜寻前不可领取。驳回方案必须记录原因，人工调整必须记录调整前后值。
15. 扇区领取通过带 expected_version 的条件更新完成，只允许分配指定船只领取未被领取的已确认扇区；并发请求至多一个成功，其余返回 409 sector_claimed。交接确认会关闭原分配、链接后继分配并保留扇区累计覆盖率。
16. 覆盖接口提交 1—10000 个基点的增量，扇区累计覆盖率上限为 10000。每个执行写请求携带 Idempotency-Key；作用域为任务、船只和操作类型。相同键与相同请求摘要返回首次结果，相同键配不同载荷返回 409 idempotency_mismatch。
17. 进度更新在单事务内写入幂等记录、校验扇区版本、累加覆盖率并追加事件；两个不同幂等键使用同一旧版本并发更新时只能有一个成功。位置上报同时更新船只位置并追加位置事件，但不得倒退最后位置时间。
18. 船只离线、剩余续航低于未完成作业加返航需求、预测版本更新或交接无接收船时产生未处理重调度建议。建议和通知使用任务、原因、船只或分配、预测版本组成的去重键，同一原因未解决前不得重复告警。
19. 疑似发现只产生事件和通知，不自动结束任务；只有指挥员可确认已找到。已找到或已终止后禁止新调度、领取和进度上报，但允许查询、通知处理和复盘导出。
20. 任务时间线按任务内递增 sequence 只追加，记录状态变化、拒绝尝试、预测生成、方案决策、人工调整、领取、位置、进度、交接、异常、通知和结论。数据库触发器拒绝更新时间线或删除事件。
21. 任务列表默认按 created_at 降序、task_id 降序稳定排序，支持状态、浮标编号和时间筛选及 page/page_size 分页。覆盖率按指定扇区版本的面积加权；船只利用率为查询窗口内已确认或执行区间并集占窗口的比例；响应时长为任务进入待调度到首次进入扇区的时差；终止原因分布只统计已终止任务。

## 数据模型与持久化

1. buoys：id、buoy_no、device_type、last_communication_at、last_latitude、last_longitude、battery_basis_points、lost_reason、version、created_at、updated_at。
2. search_tasks：id、buoy_id、status、submitted_at、found_at、found_position、termination_reason、active_sector_set_version、version、event_sequence、created_by、created_at、updated_at，并带活动任务部分唯一索引。
3. current_snapshots：id、task_id、effective_at、direction_millidegrees、speed_milliknots、uncertainty_millinautical_miles、created_by、created_at；记录不可更新。
4. sector_sets 与 search_sectors：任务内版本、快照引用、算法版本、规范化输入、输入摘要、预测中心；扇区保存编号、优先级、GeoJSON 多边形、面积、质心、累计覆盖基点、领取状态和版本。
5. vessels：id、vessel_no、位置及位置时间、speed_milliknots、endurance_seconds、max_operation_millinautical_miles、online_status、last_heartbeat_at、active_load、version。
6. schedule_plans 与 assignments：方案类型、状态、生成基准时间、评分明细、决策人与理由；分配保存扇区、船只、计划区间、状态、来源分配、领取者、实际进入退出时间、退出原因和版本。
7. handoff_requests：source_assignment_id、目标船只或空值、原因、状态、请求者、确认者、successor_assignment_id、effective_at，用于形成可追踪的扇区交接链。
8. execution_reports 与 idempotency_records：保存上报类型、业务载荷、请求摘要、响应状态与响应体；唯一键覆盖任务、船只、操作类型和幂等键。
9. replan_suggestions：task_id、cause、vessel_id、assignment_id、from_sector_set_version、to_sector_set_version、dedupe_key、状态、处理说明和时间。
10. task_events：task_id、sequence、event_type、actor_id、actor_role、vessel_id、occurred_at、payload_json；以任务和 sequence 唯一并由触发器保护为只追加。
11. notifications：task_id、recipient_role或recipient_id、type、dedupe_key、title、payload、read_at、resolved_at、created_at；未解决 dedupe_key 唯一。
12. schema_migrations：记录迁移版本和执行时间；所有业务表、幂等响应、通知及重调度建议均位于同一持久数据库文件中。

## 公开接口

1. 服务启动接口：server 子命令读取 HTTP 地址、SQLite 文件路径、心跳阈值和巡检周期；GET /healthz 仅检查进程，GET /readyz 验证数据库及迁移状态。
2. 浮标与任务 API：POST/GET/PATCH /v1/buoys，POST/GET /v1/tasks，GET /v1/tasks/{id}，POST /v1/tasks/{id}/snapshots，POST /v1/tasks/{id}/sector-sets，POST /v1/tasks/{id}/transitions。
3. 船队 API：POST/GET/PATCH /v1/vessels，POST /v1/vessels/{id}/heartbeats；心跳可携带位置与剩余续航，并按报告时间拒绝旧数据覆盖新数据。
4. 调度 API：POST /v1/tasks/{id}/schedule-plans:auto，PATCH /v1/schedule-plans/{id}/assignments/{assignment_id}，POST /v1/schedule-plans/{id}/confirm，POST /v1/schedule-plans/{id}/reject。
5. 执行 API：POST /v1/assignments/{id}/claim、/enter、/progress、/sightings、/exit，POST /v1/assignments/{id}/positions，POST /v1/assignments/{id}/handoffs，POST /v1/handoffs/{id}/confirm。执行写接口要求 Idempotency-Key，并在请求体携带 expected_version。
6. 审计与通知 API：GET /v1/tasks/{id}/events 支持 event_type、vessel_id、from、to、page 和 page_size；GET /v1/notifications、POST /v1/notifications/{id}/read、POST /v1/notifications/{id}/resolve。
7. 查询与统计 API：GET /v1/tasks 支持稳定分页和筛选，GET /v1/tasks/{id}/live，GET /v1/statistics/coverage、/vessel-utilization、/response-time、/termination-reasons，GET /v1/tasks/{id}/review 导出确定字段顺序的 JSON 复盘摘要。
8. 统一协议：成功响应包含 data、version 和 request_id；错误响应包含 code、message、details、current_version 与 request_id。主要状态码为 400 参数错误、403 角色越权、404 不存在、409 状态或并发冲突、422 业务能力不足、503 存储暂不可用。

## 失败边界

1. 任何涉及状态、方案确认、领取、进度或交接的多表修改均由单个数据库事务完成；事件追加失败时业务写一并回滚，避免出现无法审计的成功操作。
2. 非法转换、分配冲突等预期业务拒绝在短独立事务中记录拒绝事件或通知后返回错误；若该审计事务也失败，返回 503 audit_unavailable，不伪装成普通业务冲突。
3. SQLite 启用外键、WAL、busy_timeout 和单连接写入约束；写锁超过有限重试窗口时返回 503 storage_busy，不在内存中排队无限等待。
4. 扇区推演先完成全部输入验证和内存计算，再一次性保存扇区集合；任一非法数值、越界坐标或数据库失败都不得留下半个版本。
5. 幂等记录与业务结果同事务提交；服务器在提交结果未知时通过幂等键重新读取已存响应，使客户端安全重试而不重复累加。
6. 心跳巡检和重调度扫描以单批事务及条件更新运行；单个任务失败只记录运行日志并留待下一轮，不终止 HTTP 服务，也不撤销其他任务已提交的结果。
7. 进程重启后先迁移数据库，再根据持久化心跳重新判定在线状态、恢复未解决建议和通知，最后启动巡检；正确性不依赖内存缓存或未持久化定时器。
8. 复盘和统计均从已提交业务表与时间线读取同一数据库快照；查询超时只返回错误，不生成部分导出文件或改变任务状态。

## 验收标准

1. 仓库包含 go.mod、可重复数据库迁移和多阶段 Dockerfile，可使用 buildx 构建 linux/amd64 与 linux/arm64 镜像；生产代码至少 20 个 Go 文件、4 个有业务意义的包，约 2500 行且保持在 2000—3000 有效行范围。
2. 可通过 HTTP 完整演示浮标建档、任务提交、预测推演、自动调度、方案确认、船只领取与执行、新预测触发重调度、确认找到以及复盘导出的闭环。
3. 单浮标活动任务约束和六状态状态机得到执行；非法转换、终态写入及过期 expected_version 均返回明确 409 错误，且拒绝尝试可在时间线追溯。
4. 固定浮标、时间与预测输入总是得到相同中心坐标、四个有序扇区、优先级、摘要和版本数据；生成新版本不修改旧版本的几何、分配或覆盖记录。
5. 自动调度结果排序稳定并排除离线、超距或续航不足船只；方案确认拒绝所有时间重叠，并发领取同一扇区时至多一个请求成功，人工调整与交接均保留完整记录。
6. 相同幂等键和载荷的执行请求返回首次结果且只累计一次，相同键不同载荷被拒绝；不同请求并发更新同一旧版本时仅一个成功，船只离线、续航不足和预测更新均产生去重的重调度建议与通知。
7. 时间线可按事件类型、船只和 UTC 时间范围稳定分页查询；通知支持已读、解决及未处理统计；覆盖率、利用率、平均响应时长、终止原因分布和 JSON 复盘结果均符合已定义口径。
8. 服务使用持久数据库文件重启后，任务版本、旧扇区、执行进度、幂等结果、未处理建议和事件顺序均保持不变，并能立即依据持久化心跳补做离线判定而不重复告警。

## 确定性测试场景

1. internal/mission/service_test.go：同一浮标创建第二个活动任务失败，而首个任务终止后允许创建新任务。
2. internal/mission/service_test.go：覆盖全部合法状态路径，并验证终态不能再次转换。
3. internal/mission/service_test.go：非法转换返回 invalid_transition 且只追加一条拒绝事件，不增加任务版本。
4. internal/mission/service_test.go：两个请求使用相同任务旧版本时仅首个修改成功，另一个得到 stale_version。
5. internal/drift/engine_test.go：固定坐标、失联时长、流向、流速和半径得到逐坐标精确到六位的 fan-v1 四扇区夹具。
6. internal/drift/engine_test.go：相同输入重复推演具有相同摘要与几何，但生成递增集合版本且旧版本保持不变。
7. internal/drift/engine_test.go：拒绝早于最后通信的生效时间、极区纬度、非法方向、零速度及非正不确定半径。
8. internal/fleet/scheduler_test.go：候选船只评分相同时按船只编号稳定选择，并按扇区优先级生成方案。
9. internal/fleet/scheduler_test.go：离线、往返超距和续航不足的船只均被排除并给出可定位的不可分配原因。
10. internal/fleet/scheduler_test.go：确认人工调整后的重叠区间返回 schedule_overlap，首尾相接区间允许确认并产生冲突通知统计。
11. internal/fleet/scheduler_test.go：两个并发方案使用相同任务版本确认时只有一个提交，数据库中不存在重叠分配。
12. internal/execution/service_test.go：相同幂等键重复提交覆盖增量只累计一次，并返回相同响应。
13. internal/execution/service_test.go：复用幂等键提交不同进度载荷返回 idempotency_mismatch。
14. internal/execution/service_test.go：两个 goroutine 并发领取同一扇区时恰好一个成功，失败方得到 sector_claimed。
15. internal/execution/service_test.go：两个不同幂等键使用相同扇区版本更新进度时仅一个成功，覆盖率不发生丢失更新。
16. internal/execution/service_test.go：离线巡检、续航下降、预测更新和无接收方交接分别产生一次去重重调度建议。
17. internal/api/integration_test.go：使用固定时钟完成全流程，验证角色权限、任务详情、疑似发现通知、确认找到与复盘摘要。
18. internal/api/integration_test.go：关闭并重新打开临时 SQLite 文件后，幂等重试仍返回原响应，进度、时间线序号和未解决通知保持一致。
19. internal/api/integration_test.go：验证任务稳定分页、时间线组合筛选以及覆盖率、利用率、响应时长和终止原因统计的固定夹具结果。

## 组件追踪关系

1. HTTP 接入与角色控制映射到 cmd/server 1 个文件及 internal/api 5 个文件，约 430 行，覆盖路由、中间件、错误映射、任务与船队处理器以及查询导出。
2. 浮标与任务控制映射到 internal/mission 4 个文件，约 390 行，分别承担模型、状态机、浮标服务与任务服务。
3. 漂移推演与版本管理映射到 internal/drift 2 个文件，约 260 行，集中实现输入规范化、几何计算、摘要及版本结果。
4. 船队与调度映射到 internal/fleet 3 个文件，约 430 行，覆盖船只模型与心跳、能力判定、评分和方案确认。
5. 执行协同与重调度映射到 internal/execution 3 个文件，约 340 行，覆盖幂等上报、领取与交接、离线和续航巡检。
6. 审计、通知与持久恢复映射到 internal/audit 3 个文件及 internal/store 4 个文件，约 650 行，覆盖时间线、通知、统计复盘、迁移、事务和仓储；全项目合计约 25 个生产 Go 文件与 2500 行有效代码。

## 独特性

项目以失联漂流浮标的不可变海流预测版本为起点，将确定性扇区推演、船只能力调度、海上交接、预测更新重调度和复盘审计串成单一闭环，核心矛盾是执行中搜索区域持续演化而历史任务必须保持可追溯。
