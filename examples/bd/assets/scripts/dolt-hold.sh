#!/bin/sh

# dolt_hold_blocks returns 0 (true) when a managed-Dolt hold forbids starting,
# recovering or restarting the server, and sets DOLT_HOLD_MESSAGE to a one-line
# explanation. It returns 1 (false) when there is no hold, or the hold has
# expired.
#
# A hold means "Dolt is down on purpose": a database directory is being
# swapped, a GC is running offline, a restore is being rehearsed. Without it the
# controller tick, the bd retry and the city's watchdogs bring a stopped Dolt
# back within seconds (ga-3risy2).
#
# The hold is the file $PACK_STATE_DIR/dolt-hold (GC_DOLT_HOLD_FILE overrides).
# Same file and same rules as cmd/gc/dolt_hold.go; keep the two in step:
#   - optional "key=value" lines, "#" starts a comment line;
#   - expires_at=<unix seconds, digits only> releases the hold at that time;
#   - owner= and reason= are shown in the message;
#   - an empty file (a bare touch) holds until it is removed.
#
# There are three answers, not two: no hold, hold, and "could not tell". The
# last one blocks. What a hold guards is the destructive action (starting or
# killing a Dolt on a directory somebody is swapping), so under doubt the inert
# answer is "do not touch Dolt". Only an absent file or a passed expiry
# releases it.
#
# Needs PACK_STATE_DIR unless GC_DOLT_HOLD_FILE is set. Read-only: it never
# removes an expired file.
dolt_hold_blocks() {
    DOLT_HOLD_MESSAGE=""
    _dh_file="${GC_DOLT_HOLD_FILE:-}"
    if [ -z "$_dh_file" ]; then
        if [ -z "${PACK_STATE_DIR:-}" ]; then
            # No path to look at: cannot know whether a hold exists.
            DOLT_HOLD_MESSAGE="dolt hold: pack state dir unknown, cannot look for the hold file; treating it as a hold"
            return 0
        fi
        _dh_file="$PACK_STATE_DIR/dolt-hold"
    fi

    # Parent directory really absent -> nobody can have placed a hold there.
    # [ -e ] is false for ENOENT but ALSO for EACCES/ENOTDIR on a component
    # above the parent, which Go (os.Open) reads as unreadable; so "absent"
    # only holds when the nearest ancestor that does exist is a searchable
    # directory. Parent present but not a directory, or not searchable ->
    # unreadable (blocks), as ENOTDIR/EACCES are in Go. File absent -> no
    # hold. File present but not a readable regular file -> unreadable.
    # (A symlink above the parent that leads nowhere holds here, as it does in
    # Go: something is on the path, so nothing can say "no hold".)
    # empty (no directory named) or failed (dirname) -> unreadable, never "absent"
    _dh_dir=$(dirname "$_dh_file") || _dh_dir=""
    if [ -z "$_dh_dir" ]; then
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read (could not tell its directory); treating it as a hold"
        return 0
    fi
    if [ ! -e "$_dh_dir" ] && [ ! -h "$_dh_dir" ]; then
        _dh_anc="$_dh_dir"
        while [ ! -e "$_dh_anc" ] && [ ! -h "$_dh_anc" ]; do
            _dh_up=$(dirname "$_dh_anc") || _dh_up=""
            if [ -z "$_dh_up" ] || [ "$_dh_up" = "$_dh_anc" ]; then
                break
            fi
            _dh_anc="$_dh_up"
        done
        if { [ -e "$_dh_anc" ] || [ -h "$_dh_anc" ]; } && [ -d "$_dh_anc" ] && [ -x "$_dh_anc" ]; then
            return 1 # the nearest existing ancestor is searchable: really absent
        fi
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read ($_dh_anc is not a searchable directory); treating it as a hold"
        return 0
    fi
    if [ ! -d "$_dh_dir" ]; then
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read ($_dh_dir is not a directory); treating it as a hold"
        return 0
    fi
    if [ ! -x "$_dh_dir" ]; then
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read (directory not searchable); treating it as a hold"
        return 0
    fi
    if [ ! -e "$_dh_file" ] && [ ! -h "$_dh_file" ]; then
        return 1
    fi
    if [ ! -f "$_dh_file" ] || [ ! -r "$_dh_file" ]; then
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read (not a readable regular file); treating it as a hold"
        return 0
    fi

    # Empty file -> hold with no expiry; read failure -> unreadable (blocks).
    # Capped at 4 KiB like the Go reader, so a wrong file is not slurped.
    if ! _dh_content=$(LC_ALL=C head -c 4096 "$_dh_file" 2>/dev/null); then
        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file could not be read; treating it as a hold"
        return 0
    fi

    _dh_expires=""
    _dh_owner=""
    _dh_reason=""
    _dh_saw_expiry=0
    while IFS= read -r _dh_line; do
        _dh_line=${_dh_line#"${_dh_line%%[![:space:]]*}"}
        _dh_line=${_dh_line%"${_dh_line##*[![:space:]]}"}
        case "$_dh_line" in
            ''|'#'*) continue ;;
            *=*) ;;
            *) continue ;;
        esac
        _dh_key=${_dh_line%%=*}
        _dh_val=${_dh_line#*=}
        _dh_key=${_dh_key%"${_dh_key##*[![:space:]]}"}
        _dh_val=${_dh_val#"${_dh_val%%[![:space:]]*}"}
        case "$_dh_key" in
            expires_at)
                [ "$_dh_saw_expiry" -eq 1 ] && continue # first one wins, as in Go
                _dh_saw_expiry=1
                # No expires_at line -> holds until removed. Blank, non-digit
                # or zero -> unreadable (blocks): the hold is there but when
                # to release it is unknown.
                case "$_dh_val" in
                    ''|*[!0-9]*)
                        DOLT_HOLD_MESSAGE="dolt hold file $_dh_file has expires_at='$_dh_val' (not a positive unix time); treating it as a hold"
                        return 0
                        ;;
                esac
                if ! [ "$_dh_val" -gt 0 ] 2>/dev/null; then
                    DOLT_HOLD_MESSAGE="dolt hold file $_dh_file has expires_at='$_dh_val' (not a positive unix time); treating it as a hold"
                    return 0
                fi
                _dh_expires="$_dh_val"
                ;;
            owner) _dh_owner="$_dh_val" ;;
            reason) _dh_reason="$_dh_val" ;;
        esac
    done <<EOF
$_dh_content
EOF

    if [ -n "$_dh_expires" ]; then
        _dh_now=$(date +%s 2>/dev/null) || _dh_now=""
        case "$_dh_now" in
            ''|*[!0-9]*)
                # The clock could not be read: cannot tell whether it expired.
                DOLT_HOLD_MESSAGE="dolt hold file $_dh_file has an expiry but the clock could not be read; treating it as a hold"
                return 0
                ;;
        esac
        if [ "$_dh_now" -ge "$_dh_expires" ]; then
            return 1 # expired -> released
        fi
    fi

    DOLT_HOLD_MESSAGE="dolt is on hold ($_dh_file)"
    [ -n "$_dh_owner" ] && DOLT_HOLD_MESSAGE="$DOLT_HOLD_MESSAGE by $_dh_owner"
    [ -n "$_dh_reason" ] && DOLT_HOLD_MESSAGE="$DOLT_HOLD_MESSAGE: $_dh_reason"
    if [ -n "$_dh_expires" ]; then
        _dh_when=$(date -u -r "$_dh_expires" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null) \
            || _dh_when=$(date -u -d "@$_dh_expires" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null) \
            || _dh_when="unix $_dh_expires"
        DOLT_HOLD_MESSAGE="$DOLT_HOLD_MESSAGE; expires $_dh_when"
    fi
    DOLT_HOLD_MESSAGE="$DOLT_HOLD_MESSAGE; remove the file to release it"
    return 0
}
