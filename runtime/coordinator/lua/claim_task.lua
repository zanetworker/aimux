-- claim_task.lua — atomically claim a pending task
-- KEYS[1] = task key (e.g. team:myteam:task:abc123)
-- KEYS[2] = team prefix (e.g. team:myteam)
-- ARGV[1] = agent_id
-- ARGV[2] = agent_role
-- ARGV[3] = task_id
--
-- Returns:
--   1  = claimed successfully
--   0  = already taken (status != pending)
--  -1  = wrong role
--  -2  = dependency not met

local task_key   = KEYS[1]
local team_prefix = KEYS[2]
local agent_id   = ARGV[1]
local agent_role = ARGV[2]
local task_id    = ARGV[3]

-- Check status: must be pending to claim
local status = redis.call('HGET', task_key, 'status')
if status ~= 'pending' then return 0 end

-- Check role (empty required_role = any role can claim)
local required_role = redis.call('HGET', task_key, 'required_role')
if required_role and required_role ~= '' and required_role ~= agent_role then
    return -1
end

-- Check dependencies: every depends_on task must be completed
local deps_json = redis.call('HGET', task_key, 'depends_on')
if deps_json and deps_json ~= '[]' and deps_json ~= '' then
    local deps = cjson.decode(deps_json)
    for _, dep_id in ipairs(deps) do
        local dep_status = redis.call('HGET', team_prefix .. ':task:' .. dep_id, 'status')
        if dep_status ~= 'completed' then return -2 end
    end
end

-- Atomically claim: update hash and remove from pending sorted set
redis.call('HSET', task_key, 'status', 'claimed', 'assignee', agent_id)
redis.call('ZREM', team_prefix .. ':tasks:pending', task_id)
return 1
