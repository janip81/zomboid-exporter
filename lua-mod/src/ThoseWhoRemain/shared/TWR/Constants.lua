-- TWR.Constants -- safe static values shared between client/server, no
-- gameplay logic. Per MOD-STRUCTURE.md section 6 ("shared" boundary):
-- constants only, nothing that reveals campaign content.
--
-- No require(), no cached cross-file locals -- same load-order lesson
-- ExporterLog's Trackers/Vehicles.lua and CalendarProbe's own header
-- documented: PZ's mod-local require() doesn't resolve reliably and
-- file execution order across a mod's own files isn't guaranteed.
-- Every reference to TWR.* from another file must be a fresh lookup at
-- actual call time, never a value captured into a local at file load.
TWR = TWR or {}
TWR.Constants = TWR.Constants or {}

-- CONFIRMED live 2026-08-11 (CalendarProbe TEST CAL-A): ItemType =
-- base:normal is the real B42 field (not the B41-style bare Type =
-- field this project originally assumed), grep-confirmed against the
-- live dedicated server pod's installed scripts.
TWR.Constants.CALENDAR_FULLTYPE = "ThoseWhoRemain.Calendar"

-- CONFIRMED live 2026-08-11 (TEST CAL-B): the July 1993 floor and
-- month-grid navigation both work as designed.
TWR.Constants.CALENDAR_FIRST_YEAR = 1993
TWR.Constants.CALENDAR_FIRST_MONTH = 7 -- July, 1-based -- matches calendar.md's own convention

TWR.Constants.MONTH_NAMES = {
    "JANUARY", "FEBRUARY", "MARCH", "APRIL", "MAY", "JUNE",
    "JULY", "AUGUST", "SEPTEMBER", "OCTOBER", "NOVEMBER", "DECEMBER"
}
