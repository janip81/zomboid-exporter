-- TWR.UI.Calendar / TWRCalendarUI -- the paper calendar panel. Promoted
-- from the CalendarProbe_Dev probe (zomboid-exporter-ideas,
-- custom-items-to-create/probe-code/) once CAL-A through CAL-E were
-- all confirmed live 2026-08-11 against the real installed B42 build
-- -- see calendar.md and probe-code/REVIEW.md for the full
-- verification history. This file carries forward everything proven:
-- item detection, date-grid rendering (July 1993 floor, today/past/
-- future, month navigation, leap years via the real
-- getGameTime():daysInMonth()), and the modData persistence + pen-gate
-- mechanism (item:getModData(), item:syncItemFields(),
-- ItemTag.WRITE/PEN/PENCIL/*_PEN via containsTagRecurse -- all
-- grep-confirmed against vanilla's own ISReadABook.lua/ISMap.lua).
--
-- Manual note entry (2026-08-18): click a day cell -> "day detail" view
-- -> free-text entry via the real vanilla ISTextEntryBox
-- (client/ISUI/ISTextEntryBox.lua, grep-confirmed constructor/getText()/
-- setMultipleLine()), pen-gated via playerHasWritingTool() (already
-- built), persisted via addMark() (already built) -- per
-- custom-items-to-create/calendar-manual-marking-decision.md's design:
-- the player chooses a day and types free text, not an automatic/
-- pending mark from a lore discovery. Rendering is deliberately
-- content-agnostic: this file has no idea whether a given mark's text
-- came from the player's own typing or was DB/quest-authored via
-- addMark() from elsewhere -- it just renders whatever strings are
-- present in the item's own modData, per the architecture rule that
-- Lua stays a presentation profile, not a place for quest lore.
--
-- No require(), no cached cross-file locals -- see TWR.Constants'
-- header for why.
require "ISUI/ISPanel"
require "ISUI/ISButton"
require "ISUI/ISTextEntryBox"

TWR = TWR or {}
TWR.UI = TWR.UI or {}

local function safeCall(obj, methodName, ...)
    if not obj then return false, nil end
    local method = obj[methodName]
    if type(method) ~= "function" then return false, nil end
    local ok, v = pcall(method, obj, ...)
    if ok then return true, v end
    print("TWR: " .. methodName .. "() call failed: " .. tostring(v))
    return false, nil
end

local function compareDates(a, b)
    if a.year ~= b.year then return a.year < b.year and -1 or 1 end
    if a.month ~= b.month then return a.month < b.month and -1 or 1 end
    if a.day ~= b.day then return a.day < b.day and -1 or 1 end
    return 0
end

local function isBeforeJuly1993(year, month)
    return year < TWR.Constants.CALENDAR_FIRST_YEAR
        or (year == TWR.Constants.CALENDAR_FIRST_YEAR and month < TWR.Constants.CALENDAR_FIRST_MONTH)
end

local function nextMonth(year, month)
    month = month + 1
    if month > 12 then
        month = 1
        year = year + 1
    end
    return year, month
end

local function previousMonth(year, month)
    month = month - 1
    if month < 1 then
        month = 12
        year = year - 1
    end
    return year, month
end

-- No vanilla weekday-of-month helper exists anywhere in the installed
-- B42 tree (grepped getDayOfWeek/dayOfWeek -- only a hardcoded stub in
-- server/PanelBridge.lua) -- pure Lua math, Sakamoto's algorithm.
-- 0=Sunday ... 6=Saturday.
local function weekdayForDate(year, month, day)
    local offsets = {0, 3, 2, 5, 0, 3, 5, 1, 4, 6, 2, 4}
    local y = year
    if month < 3 then y = y - 1 end
    return (y + math.floor(y / 4) - math.floor(y / 100) + math.floor(y / 400)
        + offsets[month] + day) % 7
end

-- CONFIRMED real (client/erosion/debug/DebugDemoTime.lua): expects
-- ZERO-based month, same as getGameTime():getMonth() itself.
local function daysInMonth(year, month1based)
    local ok, count = safeCall(getGameTime(), "daysInMonth", year, month1based - 1)
    if ok and type(count) == "number" and count > 0 then return count end
    if month1based == 2 then
        return (year % 400 == 0 or (year % 100 ~= 0 and year % 4 == 0)) and 29 or 28
    end
    if month1based == 4 or month1based == 6 or month1based == 9 or month1based == 11 then return 30 end
    return 31
end

local function getCurrentWorldDate()
    local gt = getGameTime()
    local okY, year = safeCall(gt, "getYear")
    local okM, month0 = safeCall(gt, "getMonth")
    local okD, day = safeCall(gt, "getDay")
    if not (okY and okM and okD) then return nil end
    return { year = year, month = month0 + 1, day = day }
end

local function dateKey(year, month, day)
    return string.format("%04d-%02d-%02d", year, month, day)
end

-- CONFIRMED real (grepped shared/TimedActions/ISReadABook.lua, which
-- stores learnedRecipe/literatureTitle/printMedia this exact way).
local function ensureCalendarData(item)
    local okMd, md = safeCall(item, "getModData")
    if not okMd or not md then return nil end
    md.twrCalendar = md.twrCalendar or {}
    md.twrCalendar.marks = md.twrCalendar.marks or {}
    return md.twrCalendar
end

local function getMarksForDate(item, year, month, day)
    local data = ensureCalendarData(item)
    if not data then return {} end
    return data.marks[dateKey(year, month, day)] or {}
end

-- Dedup by sourceId so the same discovery/note can't be written twice.
-- A date may hold multiple independent notes (different sourceId).
local function addMark(item, year, month, day, sourceId, text)
    local data = ensureCalendarData(item)
    if not data then return false end
    local key = dateKey(year, month, day)
    data.marks[key] = data.marks[key] or {}
    for _, existing in ipairs(data.marks[key]) do
        if existing.sourceId == sourceId then return false end
    end
    table.insert(data.marks[key], { sourceId = sourceId, text = text, ink = "pencil" })
    -- CONFIRMED real (grepped shared/Fishing/FishingRod.lua) -- the
    -- MP-sync call for a modData change on an InventoryItem.
    safeCall(item, "syncItemFields")
    return true
end
TWR.UI.CalendarAddMark = addMark -- exposed for other callers (e.g. a future DB/quest-driven annotation) to call directly

-- Monotonic per-item counter so multiple player-typed notes on
-- different (or the same) days never collide on addMark()'s
-- sourceId-based dedup key -- deterministic, no timestamp/randomness
-- needed since it's scoped to this one physical item's own modData.
local function nextPlayerNoteSourceId(item)
    local data = ensureCalendarData(item)
    if not data then return nil end
    data.nextNoteSeq = (data.nextNoteSeq or 0) + 1
    return "player_note_" .. tostring(data.nextNoteSeq)
end

-- CONFIRMED real via grep, client/ISUI/Maps/ISMap.lua:canWrite() --
-- mirrors vanilla's own exact pen-availability check (also used for
-- map annotation) rather than a hand-maintained item list, per
-- calendar.md's own suggestion.
local function playerHasWritingTool()
    local okP, player = pcall(function() return getPlayer() end)
    if not okP or not player then return false end
    local okInv, inv = safeCall(player, "getInventory")
    if not okInv or not inv then return false end
    local okTag, hasTool = pcall(function()
        return inv:containsTagRecurse(ItemTag.WRITE)
            or inv:containsTagRecurse(ItemTag.PEN)
            or inv:containsTagRecurse(ItemTag.PENCIL)
            or inv:containsTagRecurse(ItemTag.BLUE_PEN)
            or inv:containsTagRecurse(ItemTag.RED_PEN)
            or inv:containsTagRecurse(ItemTag.GREEN_PEN)
    end)
    return okTag and hasTool or false
end
TWR.UI.CalendarPlayerHasWritingTool = playerHasWritingTool -- exposed for the future manual-note UI to call

TWRCalendarUI = ISPanel:derive("TWRCalendarUI")

function TWRCalendarUI:new(x, y, width, height, calendarItem)
    local o = ISPanel.new(self, x, y, width, height)
    setmetatable(o, self)
    self.__index = self
    o.moveWithMouse = true
    o.calendarItem = calendarItem
    o.backgroundColor = { r = 0.88, g = 0.83, b = 0.70, a = 1.0 }
    o.borderColor = { r = 0.2, g = 0.2, b = 0.2, a = 1.0 }

    -- "grid" (month view, default) or "dayDetail" (a single day's notes
    -- + free-text entry, per calendar-manual-marking-decision.md).
    o.viewMode = "grid"
    o.selectedDay = nil

    local today = getCurrentWorldDate()
    if today then
        o.viewYear = today.year
        o.viewMonth = today.month
    else
        o.viewYear = TWR.Constants.CALENDAR_FIRST_YEAR
        o.viewMonth = TWR.Constants.CALENDAR_FIRST_MONTH
    end
    if isBeforeJuly1993(o.viewYear, o.viewMonth) then
        o.viewYear = TWR.Constants.CALENDAR_FIRST_YEAR
        o.viewMonth = TWR.Constants.CALENDAR_FIRST_MONTH
    end

    return o
end

function TWRCalendarUI:initialise()
    ISPanel.initialise(self)

    local okClose, closeBtn = pcall(function()
        return ISButton:new(self.width / 2 - 40, self.height - 36, 80, 24, "Close", self, TWRCalendarUI.onClose)
    end)
    if okClose and closeBtn then
        closeBtn:initialise()
        self:addChild(closeBtn)
        self.closeButton = closeBtn
    end

    local okPrev, prevBtn = pcall(function()
        return ISButton:new(14, 10, 30, 24, "<", self, TWRCalendarUI.onPreviousMonth)
    end)
    if okPrev and prevBtn then
        prevBtn:initialise()
        self:addChild(prevBtn)
        self.prevButton = prevBtn
    end

    local okNext, nextBtn = pcall(function()
        return ISButton:new(self.width - 44, 10, 30, 24, ">", self, TWRCalendarUI.onNextMonth)
    end)
    if okNext and nextBtn then
        nextBtn:initialise()
        self:addChild(nextBtn)
        self.nextButton = nextBtn
    end

    self:updateNavigationState()
end

function TWRCalendarUI:updateNavigationState()
    if not self.prevButton then return end
    local atFloor = self.viewYear == TWR.Constants.CALENDAR_FIRST_YEAR and self.viewMonth == TWR.Constants.CALENDAR_FIRST_MONTH
    safeCall(self.prevButton, "setEnable", not atFloor)
end

function TWRCalendarUI:onPreviousMonth()
    self:exitDayDetail()
    local year, month = previousMonth(self.viewYear, self.viewMonth)
    if isBeforeJuly1993(year, month) then return end
    self.viewYear = year
    self.viewMonth = month
    self:updateNavigationState()
end

function TWRCalendarUI:onNextMonth()
    self:exitDayDetail()
    self.viewYear, self.viewMonth = nextMonth(self.viewYear, self.viewMonth)
    self:updateNavigationState()
end

function TWRCalendarUI:onClose()
    self:removeFromUIManager()
end

local DAY_NAMES = { "MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN" }

-- Shared grid geometry -- used by both prerender() (drawing) and
-- dayAtPosition() (click hit-testing) so the two can never drift apart.
function TWRCalendarUI:dayGridGeometry()
    local left, top = 20, 50
    local cellW = (self.width - 40) / 7
    local cellH = 26
    local count = daysInMonth(self.viewYear, self.viewMonth)
    -- Sakamoto's algorithm is Sunday-first (0=Sun); the grid here is
    -- Monday-first (column 0 = Monday), so shift by 6 mod 7.
    local firstWeekday = weekdayForDate(self.viewYear, self.viewMonth, 1)
    local mondayIndex = (firstWeekday + 6) % 7
    return left, top, cellW, cellH, mondayIndex, count
end

function TWRCalendarUI:dayCellRect(day)
    local left, top, cellW, cellH, mondayIndex = self:dayGridGeometry()
    local cellIndex = mondayIndex + (day - 1)
    local col = cellIndex % 7
    local row = math.floor(cellIndex / 7)
    return left + col * cellW, top + 22 + row * cellH, cellW, cellH
end

function TWRCalendarUI:dayAtPosition(mx, my)
    local left, top, cellW, cellH, mondayIndex, count = self:dayGridGeometry()
    if my < top + 22 then return nil end -- above the grid (day-name header row)
    for day = 1, count do
        local x, y = self:dayCellRect(day)
        if mx >= x and mx < x + cellW and my >= y and my < y + cellH then
            return day
        end
    end
    return nil
end

function TWRCalendarUI:onMouseDown(x, y)
    if self.viewMode == "grid" then
        local day = self:dayAtPosition(x, y)
        if day then
            self:enterDayDetail(day)
            return
        end
    end
    -- Not a day-cell click (or already in day-detail view) -- fall back
    -- to the normal ISPanel drag-the-window behavior.
    ISPanel.onMouseDown(self, x, y)
end

-- Builds the day-detail sub-view: existing marks for that day (rendered
-- as-is, content-agnostic per this file's header) plus a free-text
-- entry box and Save button, pen-gated exactly like the rest of this
-- mechanic already is.
function TWRCalendarUI:enterDayDetail(day)
    self.viewMode = "dayDetail"
    self.selectedDay = day

    local canWrite = playerHasWritingTool()

    local entryY = self.height - 96
    local okEntry, entry = pcall(function()
        return ISTextEntryBox:new("", 20, entryY, self.width - 40, 44)
    end)
    if okEntry and entry then
        entry:initialise()
        entry:instantiate()
        safeCall(entry, "setMultipleLine", true)
        safeCall(entry, "setEditable", canWrite)
        self:addChild(entry)
        self.noteEntry = entry
    end

    local okSave, saveBtn = pcall(function()
        return ISButton:new(20, self.height - 46, 90, 24, canWrite and "Save" or "Need a pen", self, TWRCalendarUI.onSaveNote)
    end)
    if okSave and saveBtn then
        saveBtn:initialise()
        safeCall(saveBtn, "setEnable", canWrite)
        self:addChild(saveBtn)
        self.saveButton = saveBtn
    end

    local okBack, backBtn = pcall(function()
        return ISButton:new(self.width - 90, self.height - 46, 70, 24, "Back", self, TWRCalendarUI.exitDayDetail)
    end)
    if okBack and backBtn then
        backBtn:initialise()
        self:addChild(backBtn)
        self.backButton = backBtn
    end
end

function TWRCalendarUI:exitDayDetail()
    if self.viewMode ~= "dayDetail" then return end
    if self.noteEntry then safeCall(self, "removeChild", self.noteEntry); self.noteEntry = nil end
    if self.saveButton then safeCall(self, "removeChild", self.saveButton); self.saveButton = nil end
    if self.backButton then safeCall(self, "removeChild", self.backButton); self.backButton = nil end
    self.viewMode = "grid"
    self.selectedDay = nil
end

function TWRCalendarUI:onSaveNote()
    if not self.calendarItem or not self.selectedDay or not self.noteEntry then return end
    if not playerHasWritingTool() then return end -- re-checked at save time, not just at panel-open time

    local okText, text = safeCall(self.noteEntry, "getText")
    if not okText or not text or text == "" then return end

    local sourceId = nextPlayerNoteSourceId(self.calendarItem)
    if not sourceId then return end

    addMark(self.calendarItem, self.viewYear, self.viewMonth, self.selectedDay, sourceId, text)
    -- Return to the grid view -- updateNavigationState-style refresh
    -- happens implicitly since prerender() re-reads getMarksForDate()
    -- every frame.
    self:exitDayDetail()
end

function TWRCalendarUI:prerender()
    ISPanel.prerender(self)

    if self.viewMode == "dayDetail" then
        self:prerenderDayDetail()
        return
    end

    local title = TWR.Constants.MONTH_NAMES[self.viewMonth] .. " " .. tostring(self.viewYear)
    self:drawTextCentre(title, self.width / 2, 14, 0.15, 0.14, 0.12, 1.0, UIFont.Medium)

    local left, top, cellW, cellH, mondayIndex, count = self:dayGridGeometry()

    for i, name in ipairs(DAY_NAMES) do
        self:drawTextCentre(name, left + (i - 1) * cellW + cellW / 2, top, 0.25, 0.22, 0.18, 1.0, UIFont.Small)
    end

    local today = getCurrentWorldDate()

    for day = 1, count do
        local x, y = self:dayCellRect(day)

        local thisDate = { year = self.viewYear, month = self.viewMonth, day = day }
        local isPast = today and compareDates(thisDate, today) < 0
        local isToday = today and compareDates(thisDate, today) == 0

        if isPast then
            self:drawText(tostring(day), x + 6, y + 4, 0.45, 0.45, 0.45, 1.0, UIFont.Small)
            self:drawLine2(x + 4, y + 3, x + cellW - 8, y + 18, 1.0, 0.45, 0.20, 0.18)
        elseif isToday then
            self:drawRectBorder(x + 2, y, cellW - 6, 22, 1.0, 0.75, 0.15, 0.12)
            self:drawText(tostring(day), x + 6, y + 4, 0.1, 0.1, 0.1, 1.0, UIFont.Small)
        else
            self:drawText(tostring(day), x + 6, y + 4, 0.1, 0.1, 0.1, 1.0, UIFont.Small)
        end

        if self.calendarItem then
            local marks = getMarksForDate(self.calendarItem, self.viewYear, self.viewMonth, day)
            if marks and #marks > 0 then
                self:drawText("*", x + cellW - 14, y + 2, 0.16, 0.24, 0.55, 1.0, UIFont.Small)
            end
        end
    end
end

function TWRCalendarUI:prerenderDayDetail()
    local title = TWR.Constants.MONTH_NAMES[self.viewMonth] .. " " .. tostring(self.selectedDay) .. ", " .. tostring(self.viewYear)
    self:drawTextCentre(title, self.width / 2, 14, 0.15, 0.14, 0.12, 1.0, UIFont.Medium)

    local marks = self.calendarItem and getMarksForDate(self.calendarItem, self.viewYear, self.viewMonth, self.selectedDay) or {}
    local y = 40
    if #marks == 0 then
        self:drawText("(no notes yet)", 20, y, 0.35, 0.32, 0.28, 1.0, UIFont.Small)
    else
        for _, mark in ipairs(marks) do
            self:drawText(tostring(mark.text), 20, y, 0.1, 0.1, 0.1, 1.0, UIFont.Small)
            y = y + 18
        end
    end
end

function TWRCalendarUI.open(calendarItem)
    local ok, err = pcall(function()
        local width, height = 380, 340
        local x = (getCore():getScreenWidth() - width) / 2
        local y = (getCore():getScreenHeight() - height) / 2
        local ui = TWRCalendarUI:new(x, y, width, height, calendarItem)
        ui:initialise()
        ui:addToUIManager()
    end)
    if not ok then
        print("TWR: TWRCalendarUI.open() failed: " .. tostring(err))
    end
end
