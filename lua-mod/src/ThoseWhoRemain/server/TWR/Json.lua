-- TWR.Json -- minimal DECODE-ONLY JSON parser for the quest engine's
-- dispatch payloads (Gate 1 Phase 4, server/TWR/QuestEngine.lua).
--
-- Deliberately decode-only, hand-rolled recursive descent -- no
-- loadstring()/load() anywhere near this, ever. A DB-authored
-- action_params payload is untrusted-ish input by the time it reaches
-- this file (authored by whoever can write to Postgres, not the
-- player, but still crossing a process boundary via a plain-text file)
-- and must never be capable of executing arbitrary Lua. A real parser
-- that only ever produces plain Lua tables/strings/numbers/booleans is
-- the safe way to cross that boundary -- see QuestEngine.lua's header
-- for the full fail-closed contract this exists to support.
--
-- Not a full RFC 8259 implementation: \uXXXX escapes only handle
-- codepoints < 256 (falls back to "?" otherwise) -- sufficient for the
-- plain-ASCII identifiers/strings this project's own DB content uses
-- (jobId, actionType, itemType, contentId, etc.), not intended for
-- arbitrary user-facing text.
--
-- JSON null decodes to an ABSENT table key, not a sentinel value --
-- simpler for callers (just check `if tbl.field then ... end`), and
-- sufficient since nothing in the dispatch envelope needs to
-- distinguish "explicitly null" from "key omitted".
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
-- CONFIRMED live 2026-08-11: media/lua/server/ files are ALSO loaded by
-- a connecting MP client -- see server/TWR/Debug.lua's header for the
-- full live-reproduced bug. Guarding here too, though this file has no
-- load-time side effects (pure function definitions).
if isClient() then return end

TWR = TWR or {}
TWR.Json = TWR.Json or {}

local function skipWs(ctx)
    local s, n = ctx.s, ctx.n
    local i = ctx.i
    while i <= n do
        local c = s:sub(i, i)
        if c == " " or c == "\t" or c == "\n" or c == "\r" then
            i = i + 1
        else
            break
        end
    end
    ctx.i = i
end

local function peek(ctx)
    if ctx.i > ctx.n then return nil end
    return ctx.s:sub(ctx.i, ctx.i)
end

local function expect(ctx, literal)
    local len = #literal
    if ctx.s:sub(ctx.i, ctx.i + len - 1) ~= literal then
        error("expected '" .. literal .. "' at position " .. ctx.i)
    end
    ctx.i = ctx.i + len
end

local parseValue

local function parseString(ctx)
    expect(ctx, '"')
    local parts = {}
    local s, n = ctx.s, ctx.n
    while true do
        if ctx.i > n then
            error("unterminated string")
        end
        local c = s:sub(ctx.i, ctx.i)
        if c == '"' then
            ctx.i = ctx.i + 1
            break
        elseif c == "\\" then
            local esc = s:sub(ctx.i + 1, ctx.i + 1)
            if esc == '"' then
                table.insert(parts, '"'); ctx.i = ctx.i + 2
            elseif esc == "\\" then
                table.insert(parts, "\\"); ctx.i = ctx.i + 2
            elseif esc == "/" then
                table.insert(parts, "/"); ctx.i = ctx.i + 2
            elseif esc == "b" then
                table.insert(parts, "\b"); ctx.i = ctx.i + 2
            elseif esc == "f" then
                table.insert(parts, "\f"); ctx.i = ctx.i + 2
            elseif esc == "n" then
                table.insert(parts, "\n"); ctx.i = ctx.i + 2
            elseif esc == "r" then
                table.insert(parts, "\r"); ctx.i = ctx.i + 2
            elseif esc == "t" then
                table.insert(parts, "\t"); ctx.i = ctx.i + 2
            elseif esc == "u" then
                local hex = s:sub(ctx.i + 2, ctx.i + 5)
                if #hex ~= 4 then error("invalid \\u escape") end
                local codepoint = tonumber(hex, 16)
                if not codepoint then error("invalid \\u escape") end
                if codepoint < 256 then
                    table.insert(parts, string.char(codepoint))
                else
                    table.insert(parts, "?")
                end
                ctx.i = ctx.i + 6
            else
                error("invalid escape '\\" .. tostring(esc) .. "' at position " .. ctx.i)
            end
        else
            table.insert(parts, c)
            ctx.i = ctx.i + 1
        end
    end
    return table.concat(parts)
end

local function parseNumber(ctx)
    local start = ctx.i
    local s, n = ctx.s, ctx.n
    local i = ctx.i
    if s:sub(i, i) == "-" then i = i + 1 end
    while i <= n and s:sub(i, i):match("%d") do i = i + 1 end
    if s:sub(i, i) == "." then
        i = i + 1
        while i <= n and s:sub(i, i):match("%d") do i = i + 1 end
    end
    local ec = s:sub(i, i)
    if ec == "e" or ec == "E" then
        i = i + 1
        local sign = s:sub(i, i)
        if sign == "+" or sign == "-" then i = i + 1 end
        while i <= n and s:sub(i, i):match("%d") do i = i + 1 end
    end
    local raw = s:sub(start, i - 1)
    local num = tonumber(raw)
    if not num then error("invalid number literal at position " .. start) end
    ctx.i = i
    return num
end

local function parseObject(ctx)
    expect(ctx, "{")
    local obj = {}
    skipWs(ctx)
    if peek(ctx) == "}" then
        ctx.i = ctx.i + 1
        return obj
    end
    while true do
        skipWs(ctx)
        if peek(ctx) ~= '"' then error("expected string key at position " .. ctx.i) end
        local key = parseString(ctx)
        skipWs(ctx)
        expect(ctx, ":")
        skipWs(ctx)
        local value = parseValue(ctx)
        if value ~= nil then
            obj[key] = value
        end
        skipWs(ctx)
        local c = peek(ctx)
        if c == "," then
            ctx.i = ctx.i + 1
        elseif c == "}" then
            ctx.i = ctx.i + 1
            break
        else
            error("expected ',' or '}' at position " .. ctx.i)
        end
    end
    return obj
end

local function parseArray(ctx)
    expect(ctx, "[")
    local arr = {}
    skipWs(ctx)
    if peek(ctx) == "]" then
        ctx.i = ctx.i + 1
        return arr
    end
    while true do
        skipWs(ctx)
        local value = parseValue(ctx)
        table.insert(arr, value == nil and TWR.Json.NULL_PLACEHOLDER or value)
        skipWs(ctx)
        local c = peek(ctx)
        if c == "," then
            ctx.i = ctx.i + 1
        elseif c == "]" then
            ctx.i = ctx.i + 1
            break
        else
            error("expected ',' or ']' at position " .. ctx.i)
        end
    end
    return arr
end

-- NULL_PLACEHOLDER only shows up inside ARRAY elements (an array slot
-- can't just be "absent" the way an object key can) -- consumers of
-- array-shaped action_params fields that might contain null should
-- check `if v == TWR.Json.NULL_PLACEHOLDER then ... end`. None of the
-- Gate 1 action types use null-bearing arrays today.
TWR.Json.NULL_PLACEHOLDER = setmetatable({}, { __tostring = function() return "null" end })

parseValue = function(ctx)
    skipWs(ctx)
    local c = peek(ctx)
    if c == nil then
        error("unexpected end of input")
    elseif c == "{" then
        return parseObject(ctx)
    elseif c == "[" then
        return parseArray(ctx)
    elseif c == '"' then
        return parseString(ctx)
    elseif c == "t" then
        expect(ctx, "true")
        return true
    elseif c == "f" then
        expect(ctx, "false")
        return false
    elseif c == "n" then
        expect(ctx, "null")
        return nil
    elseif c == "-" or c:match("%d") then
        return parseNumber(ctx)
    else
        error("unexpected character '" .. c .. "' at position " .. ctx.i)
    end
end

-- Returns ok, value_or_error -- mirrors pcall/this codebase's own
-- ok,err convention (TWR.Emit.jobResult, the Container.lua safeCall
-- helper, etc.), never throws.
function TWR.Json.decode(str)
    if type(str) ~= "string" then
        return false, "TWR.Json.decode: input is not a string (" .. type(str) .. ")"
    end
    local ok, result = pcall(function()
        local ctx = { s = str, i = 1, n = #str }
        skipWs(ctx)
        local v = parseValue(ctx)
        skipWs(ctx)
        if ctx.i <= ctx.n then
            error("trailing data at position " .. ctx.i)
        end
        return v
    end)
    if ok then return true, result end
    return false, result
end
