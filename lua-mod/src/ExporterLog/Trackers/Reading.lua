-- ExporterLog.Reading -- book reading. Small module, kept separate
-- from Consumption.lua because it's logically independent (not
-- consumption) and will likely grow later (started/completed/pages
-- read is on the backlog).
--
-- No require(), no cached cross-file locals -- see Vehicles.lua's
-- header comment for why. Every ExporterLog.Runtime/Emit/Utils
-- reference below is a fresh lookup at actual call time.
ExporterLog = ExporterLog or {}
ExporterLog.Reading = ExporterLog.Reading or {}
ExporterLog.Reading.originals = ExporterLog.Reading.originals or {}

local Reading = ExporterLog.Reading

-- CONFIRMED live (2026-08-10) via the diagnostic this replaced: a
-- plain Literature book (e.g. "Clawmarks") completes in exactly one
-- session with no partial-progress state (self.page/numPages/
-- pageDelta all nil), and item:getNumberOfPages() IS a real method
-- (returned -1 for that book -- presumably "not applicable" for a
-- non-paginated book type). item:isRead()/getPercentRead()/
-- getPagesRead() are all confirmed NOT real methods on this item type.
--
-- Skill books (Carpentry/Cooking/etc, multi-session, real page counts)
-- work differently -- per external API research: item:getAlreadyReadPages()
-- tracks persistent progress on the item itself, and the action stores
-- where THIS session started in self.startPage. Neither is confirmed
-- live yet (server still offline) -- treated with the same
-- existence-check-before-call discipline the isRead() debugger freeze
-- taught: a plain pcall alone isn't enough, since PZ's Lua debugger's
-- "break on error" pauses the WHOLE GAME the instant a bad method call
-- throws, even one a pcall would otherwise safely catch a moment
-- later. safeCall() below checks obj[methodName] is actually a
-- function BEFORE ever calling it -- a plain table lookup, which never
-- errors in Lua even for a nonexistent key.
local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    return false, nil
end

-- UNVERIFIED (2026-08-10): SkillBook global + item:getSkillTrained()
-- are unconfirmed claims, unlike item:getNumberOfPages() (confirmed
-- above). Defensive on both: a missing global or a missing/erroring
-- method just means "not a skill book" (falls back to literature
-- handling) rather than erroring.
local function isSkillBook(item)
    if not item then return false end
    local okPages, numPages = safeCall(item, "getNumberOfPages")
    if not okPages or type(numPages) ~= "number" or numPages <= 0 then return false end
    if type(_G.SkillBook) ~= "table" then return false end
    local okSkill, skillName = safeCall(item, "getSkillTrained")
    if not okSkill or not skillName then return false end
    return _G.SkillBook[skillName] ~= nil
end

-- Shared by both the .stop() (interrupted) and .complete() (finished)
-- hooks below -- whichever fires FIRST for a given reading session
-- does the actual emit and marks self.exporterLogHandled, so if the
-- underlying TimedAction lifecycle ever calls both for the same
-- session, this never double-emits. self is a fresh TimedAction
-- instance per reading session (confirmed by the "resume from page 4"
-- test earlier), so this flag can't leak across sessions.
local function handleReadSessionEnd(self)
    if self.exporterLogHandled then return end
    if not self.item then return end

    local username = (self.character and self.character.getUsername) and self.character:getUsername() or "?"
    local steamId = ExporterLog.Utils.getPlayerSteamID(self.character)
    local itemFullType = self.item:getFullType() or "?"
    local itemName = ExporterLog.Utils.getItemDisplayName(self.item)

    if isSkillBook(self.item) then
        local okEnd, pageEnd = safeCall(self.item, "getAlreadyReadPages")
        if not okEnd or type(pageEnd) ~= "number" then return end

        local pageStart = self.startPage
        if type(pageStart) ~= "number" then pageStart = pageEnd end

        local pagesRead = pageEnd - pageStart
        if pagesRead <= 0 then return end

        local okTotal, totalPages = safeCall(self.item, "getNumberOfPages")
        local completed = okTotal and type(totalPages) == "number" and totalPages > 0 and pageEnd >= totalPages or false

        self.exporterLogHandled = true
        ExporterLog.Emit.event({
            type = "read",
            username = username,
            steamId = steamId,
            item = itemFullType,
            name = itemName,
            pageStart = pageStart,
            pageEnd = pageEnd,
            pagesRead = pagesRead,
            totalPages = okTotal and totalPages or nil,
            completed = completed,
        })
    else
        -- Ordinary literature: only a genuine complete() should ever
        -- reach here with something to report -- self.forceStopped
        -- (obsolete skill book / illiterate trait) already excludes
        -- the aborted case, and .stop() firing on a book that never
        -- reached isSkillBook()'s bar just means an interrupted
        -- literature read, which has never emitted anything before
        -- this change either.
        if self.forceStopped then return end
        self.exporterLogHandled = true
        ExporterLog.Emit.event({
            type = "read",
            username = username,
            steamId = steamId,
            item = itemFullType,
            name = itemName,
            amount = 1,
        })
    end
end

-- Monkey-patches BOTH .complete() and .stop(), reload-safe (each
-- restores its own preserved original before rewrapping -- never
-- stacks, even across many F11 reloads) -- same idempotent pattern
-- Runtime.hookTimedActionOnce uses for every other tracker in this
-- mod, just inlined here since this needs two hooks sharing state
-- (handleReadSessionEnd's dedup flag) rather than one extractor per
-- hook.
function Reading.init()
    local Runtime = ExporterLog.Runtime

    if not Reading.originals["ISReadABook.complete"] then
        Reading.originals["ISReadABook.complete"] = ISReadABook.complete
    end
    local originalComplete = Reading.originals["ISReadABook.complete"]
    ISReadABook.complete = originalComplete

    ISReadABook.complete = function(self, ...)
        local result = originalComplete(self, ...)
        local ok, err = pcall(handleReadSessionEnd, self)
        if not ok then print(Runtime.logPrefix() .. ": Reading complete-hook error: " .. tostring(err)) end
        return result
    end
    print(Runtime.logPrefix() .. ": installed read hook (ISReadABook.complete)")

    -- .stop() is riskier to hook than .complete(): every OTHER
    -- TimedAction.complete this mod wraps is confirmed directly owned
    -- by its concrete subclass (proven across eight different classes
    -- already), but .stop() may instead be inherited from a shared
    -- base class rather than defined on ISReadABook itself --
    -- capturing a nil "original" and later calling it would break
    -- real reading-interrupt behavior for every player, not just our
    -- tracking. type(ISReadABook.stop)=="function" is safe to check
    -- regardless of whether the method is own or inherited (normal
    -- Lua indexing already resolves that for us) -- only wrapped if
    -- it's genuinely callable, otherwise skipped with a diagnostic.
    if type(ISReadABook.stop) == "function" then
        if not Reading.originals["ISReadABook.stop"] then
            Reading.originals["ISReadABook.stop"] = ISReadABook.stop
        end
        local originalStop = Reading.originals["ISReadABook.stop"]
        ISReadABook.stop = originalStop

        ISReadABook.stop = function(self, ...)
            local result = originalStop(self, ...)
            local ok, err = pcall(handleReadSessionEnd, self)
            if not ok then print(Runtime.logPrefix() .. ": Reading stop-hook error: " .. tostring(err)) end
            return result
        end
        print(Runtime.logPrefix() .. ": installed read hook (ISReadABook.stop)")
    else
        print(Runtime.logPrefix() .. ": ISReadABook.stop not directly hookable (type=" .. type(ISReadABook.stop) .. ") -- interrupted skill-book sessions won't be tracked, only full completions")
    end
end

-- Self-initialize: an immediate attempt handles every F11 reload
-- (which re-executes this whole file top to bottom, refreshing
-- everything). The Events.OnGameStart fallback handles the one-time
-- first-boot ordering race, where ExporterLog.Runtime/Emit might not
-- exist yet at the exact moment PZ's auto-loader happens to run THIS
-- file -- OnGameStart is confirmed to fire only once, after every mod
-- file has finished loading, and never fires again on a later reload,
-- so it can't cause double-init -- Reading.init() is idempotent
-- anyway.
local ok, err = pcall(Reading.init)
if not ok then
    print("ExporterLog: Reading.init() deferred to OnGameStart (dependency not loaded yet): " .. tostring(err))
end
Events.OnGameStart.Add(Reading.init)
