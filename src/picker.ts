import readline from "node:readline"

import { shortenHome } from "./format.js"
import { searchSessions } from "./sessions.js"
import { accountLabel, isNativeTarget, nextTarget, nextToolTarget, toolName } from "./targets.js"
import type { Account, SessionPreview, SessionSource } from "./types.js"

// "resume" continues the picked session in place; "fork" branches off it into a
// new session, leaving the original conversation as it was.
export type PickMode = "resume" | "fork"

export type PickResult = {
  session: SessionPreview
  target: Account
  mode: PickMode
  // Set only when Ctrl+Y was used: the caller's own default applies otherwise.
  skipPermissions?: boolean
}

function clearScreen() {
  process.stdout.write("\x1Bc")
}

function dim(value: string) {
  return `\x1b[2m${value}\x1b[0m`
}

function bold(value: string) {
  return `\x1b[1m${value}\x1b[0m`
}

function cyan(value: string) {
  return `\x1b[36m${value}\x1b[0m`
}

function yellow(value: string) {
  return `\x1b[33m${value}\x1b[0m`
}

function magenta(value: string) {
  return `\x1b[35m${value}\x1b[0m`
}

function blue(value: string) {
  return `\x1b[34m${value}\x1b[0m`
}

function green(value: string) {
  return `\x1b[32m${value}\x1b[0m`
}

function red(value: string) {
  return `\x1b[1;31m${value}\x1b[0m`
}

function sessionLabel(session: SessionPreview) {
  return accountLabel(session.source, session.account)
}

// The badge is the route: one label when the target is where the session
// already lives, "from→to" when following it means landing somewhere else.
export function sessionBadgeText(session: SessionPreview, target?: Account) {
  const from = sessionLabel(session)
  if (!target || isNativeTarget(session, target)) return `[${from}]`
  return `[${from}→${accountLabel(target.tool, target)}]`
}

export function enterDestination(
  session: SessionPreview,
  target?: Account,
  mode: PickMode = "resume",
): PickResult {
  return {
    session,
    target: target ?? session.account ?? { tool: session.source, name: sessionLabel(session) },
    mode,
  }
}

// While the target still follows the selection, no row is going anywhere but
// its own account, so every badge stays native. Only a pinned target turns the
// other rows into routes.
export function badgeTarget(target: Account | undefined, pinned: boolean) {
  return pinned ? target : undefined
}

const SOURCE_COLOR: Record<SessionSource, (value: string) => string> = {
  opencode: magenta,
  claude: blue,
  codex: green,
}

function badge(session: SessionPreview, target?: Account) {
  return SOURCE_COLOR[session.source](sessionBadgeText(session, target))
}

// "Claude CC2" rather than a bare label, so the hint line reads as a sentence.
function destinationName(target: Account) {
  return target.tool === "opencode"
    ? toolName(target.tool)
    : `${toolName(target.tool)} ${accountLabel(target.tool, target)}`
}

function termSize() {
  return {
    cols: process.stdout.columns || 100,
    rows: process.stdout.rows || 30,
  }
}

function truncatePlain(value: string, width: number) {
  if (width <= 1) return ""
  if (value.length <= width) return value
  return `${value.slice(0, width - 1)}…`
}

function clamp(value: number, min: number, max: number) {
  return Math.max(min, Math.min(max, value))
}

function splitTerms(query: string) {
  return query
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
}

function highlightTerms(value: string, query: string) {
  const terms = splitTerms(query)
  if (terms.length === 0) return value

  let result = value

  for (const term of terms) {
    const pattern = new RegExp(term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi")
    result = result.replace(pattern, (match) => yellow(match))
  }

  return result
}

function padLine(value: string, width: number) {
  const plain = value.replace(/\x1b\[[0-9;]*m/g, "")
  if (plain.length >= width) return value
  return `${value}${" ".repeat(width - plain.length)}`
}

function renderColumns(left: string[], right: string[], leftWidth: number) {
  const total = Math.max(left.length, right.length)
  const lines: string[] = []

  for (let index = 0; index < total; index += 1) {
    const leftLine = padLine(left[index] ?? "", leftWidth)
    const rightLine = right[index] ?? ""
    lines.push(`${leftLine}  ${rightLine}`)
  }

  return lines.join("\n")
}

function renderPreview(session: SessionPreview, query: string, width: number) {
  // Every returned line's *plain* length must be <= width, prefixes included,
  // or the terminal wraps it back to column 0 and smears the layout.
  const put = (plain: string) => highlightTerms(truncatePlain(plain, width), query)

  const lines = [
    bold(put(session.title)),
    dim(put(shortenHome(session.directory))),
    dim(truncatePlain(
      `${session.updatedAtLabel}  ${sessionLabel(session)}  ${session.id}`,
      width,
    )),
    "",
  ]

  if (session.prompts.length > 0) {
    lines.push(cyan("Recent user prompts"))
    for (const prompt of session.prompts) {
      lines.push(put(`- ${prompt}`))
    }
    lines.push("")
  }

  if (session.assistantSnippets.length > 0) {
    lines.push(cyan("Recent assistant snippets"))
    for (const snippet of session.assistantSnippets) {
      lines.push(put(`- ${snippet}`))
    }
  }

  return lines
}

function renderList(
  items: SessionPreview[],
  activeIndex: number,
  pageStart: number,
  pageSize: number,
  query: string,
  width: number,
  target?: Account,
) {
  if (items.length === 0) return [dim("No matches")]

  const page = items.slice(pageStart, pageStart + pageSize)
  const indentWidth = Math.max(4, width - 6)

  return page.flatMap((session, index) => {
    const realIndex = pageStart + index
    const active = realIndex === activeIndex
    const marker = active ? cyan(">") : " "
    const titleWidth = Math.max(4, width - sessionBadgeText(session, target).length - 4)
    const title = truncatePlain(session.title, titleWidth)
    const dir = truncatePlain(shortenHome(session.directory), indentWidth)
    const date = truncatePlain(session.updatedAtLabel, indentWidth)

    return [
      `${marker} ${badge(session, target)} ${highlightTerms(title, query)}`,
      dim(`     ${highlightTerms(dir, query)}`),
      dim(`     ${date}`),
      "",
    ]
  })
}

// Terminals disagree on what Enter sends: a raw-mode tty usually delivers "\r"
// (name "return"), but any layer with ICRNL still on delivers "\n" (name
// "enter"). Accept both, or Enter silently falls through to the query.
export function isSubmitKey(key: readline.Key) {
  return key.name === "return" || key.name === "enter"
}

// Only printable input belongs in the query. Control bytes (Enter, Ctrl+D, …)
// that reach the catch-all would otherwise be appended invisibly and skew the
// filter.
export function queryInput(key: readline.Key) {
  if (key.ctrl || key.meta || !key.sequence) return ""
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f]/.test(key.sequence)) return ""
  return key.sequence
}

function clampIndex(index: number, length: number) {
  if (length === 0) return 0
  if (index < 0) return 0
  if (index >= length) return length - 1
  return index
}

export async function pickSession(
  sessions: SessionPreview[],
  initialQuery = "",
  options?: {
    // Every place a session can be opened, in cycling order.
    targets?: Account[]
    initialTarget?: Account
    // Whether a launch into this target bypasses permission checks when
    // Ctrl+Y has not been pressed.
    skipPermissions?: (target: Account) => boolean
  },
): Promise<PickResult> {
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  })

  readline.emitKeypressEvents(process.stdin, rl)

  if (process.stdin.isTTY) {
    process.stdin.setRawMode(true)
  }

  let query = initialQuery
  let activeIndex = 0
  let filtered = searchSessions(sessions, query)
  const targets = options?.targets ?? []

  const indexOfTarget = (wanted?: Account) =>
    wanted
      ? targets.findIndex(
          (target) => target.tool === wanted.tool && target.name === wanted.name,
        )
      : -1

  // Until the target is cycled it follows the selection, so Enter resumes what
  // you picked where it already lives. Cycling pins a destination, and from
  // then on the badge shows the route to it.
  let pinnedIndex = indexOfTarget(options?.initialTarget)

  const currentTarget = () => {
    if (pinnedIndex >= 0) return targets[pinnedIndex]
    const selected = filtered[activeIndex]
    return selected ? targets.find((target) => isNativeTarget(selected, target)) : undefined
  }

  const cycleTarget = (step: number) => {
    const from = pinnedIndex >= 0 ? pinnedIndex : indexOfTarget(currentTarget())
    pinnedIndex = nextTarget(targets, from, step)
  }

  // Ctrl+Y flips yolo for this one launch. Until then each target keeps its
  // configured default, so the hint tracks whatever the route lands on.
  let yoloOverride: boolean | undefined
  const yoloFor = (target?: Account) =>
    yoloOverride ?? (target ? (options?.skipPermissions?.(target) ?? false) : false)

  const finish = (session: SessionPreview, target: Account | undefined, mode?: PickMode) => ({
    ...enterDestination(session, target, mode),
    ...(yoloOverride === undefined ? {} : { skipPermissions: yoloOverride }),
  })

  const render = () => {
    filtered = searchSessions(sessions, query)
    activeIndex = clampIndex(activeIndex, filtered.length)

    const { cols, rows } = termSize()
    // Layout: [left padded to leftWidth][2-space gap][right]. Keep the whole
    // row <= cols-1 so no terminal wraps a line back to column 0.
    const leftWidth = clamp(Math.round(cols * 0.42), 30, 64)
    const rightWidth = Math.max(20, cols - leftWidth - 3)

    const headerRows = 6
    const linesPerItem = 4
    const availableRows = Math.max(linesPerItem, rows - headerRows - 1)
    const pageSize = Math.max(1, Math.floor(availableRows / linesPerItem))
    const pageIndex = Math.floor(activeIndex / pageSize)
    const pageStart = pageIndex * pageSize
    const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize))

    clearScreen()
    const target = currentTarget()
    const rowTarget = badgeTarget(target, pinnedIndex >= 0)
    process.stdout.write(
      `${bold("Sessions")}  ${magenta("[OC]")} ${dim("opencode")}` +
        `  ${blue("[CC*]")} ${dim("claude")}  ${green("[CX*]")} ${dim("codex")}\n`,
    )
    const selectedNow = filtered[activeIndex]
    const enterName = target ? destinationName(target) : "its own tool"
    const tabTarget = selectedNow
      ? nextToolTarget(targets, selectedNow.source, target)
      : undefined
    const openHint = `Enter: ${enterName}. Ctrl+F: fork it.${
      tabTarget ? ` Tab: ${destinationName(tabTarget)}.` : ""
    }`
    process.stdout.write(`${dim(`Type to filter. ↑↓ move. PgUp/PgDn jump. ${openHint} Esc cancels.`)}\n`)
    const targetHint = target
      ? `Target: ${accountLabel(target.tool, target)}${pinnedIndex < 0 ? " (follows the selection)" : ""}.` +
        " Ctrl+T / Shift+Tab cycle. Enter follows displayed route."
      : "No target configured."
    process.stdout.write(`${dim(targetHint)}\n`)
    process.stdout.write(`Query: ${query}\n`)
    // The permission mode sits on the short status line so the header never
    // wraps: it must stay visible, since it decides what the agent may do.
    const yoloHint = yoloFor(target)
      ? `${red("YOLO")} ${dim("permission checks bypassed, Ctrl+Y to ask again")}`
      : dim("Permissions: ask. Ctrl+Y: yolo")
    process.stdout.write(
      `${dim(`${filtered.length} matches  Page ${pageIndex + 1}/${pageCount}`)}  ${yoloHint}\n\n`,
    )

    const selected = filtered[activeIndex]
    const left = renderList(filtered, activeIndex, pageStart, pageSize, query, leftWidth, rowTarget)
    const right = selected
      ? renderPreview(selected, query, rightWidth)
      : [dim("No session selected")]

    process.stdout.write(`${renderColumns(left, right.slice(0, availableRows), leftWidth)}\n`)
  }

  return await new Promise<PickResult>((resolve, reject) => {
    const cleanup = () => {
      if (process.stdin.isTTY) {
        process.stdin.setRawMode(false)
      }

      process.stdin.removeListener("keypress", onKeypress)
      process.stdout.removeListener("resize", render)
      rl.close()
      clearScreen()
    }

    const onKeypress = (_: string, key: readline.Key) => {
      if (key.ctrl && key.name === "c") {
        cleanup()
        reject(new Error("Cancelled."))
        return
      }

      if (key.name === "escape") {
        cleanup()
        reject(new Error("Cancelled."))
        return
      }

      if (isSubmitKey(key)) {
        const selected = filtered[activeIndex]
        if (!selected) return
        cleanup()
        resolve(finish(selected, currentTarget()))
        return
      }

      if (key.ctrl && key.name === "y") {
        yoloOverride = !yoloFor(currentTarget())
        render()
        return
      }

      // Fork follows the same route as Enter, but branches into a new session
      // instead of writing more turns into the picked one.
      if (key.ctrl && key.name === "f") {
        const selected = filtered[activeIndex]
        if (!selected) return
        cleanup()
        resolve(finish(selected, currentTarget(), "fork"))
        return
      }

      // Shift+Tab walks the target list backwards, the counterpart to Ctrl+T.
      if ((key.name === "tab" && key.shift) || key.name === "backtab") {
        cycleTarget(-1)
        render()
        return
      }

      // Tab skips straight to the next tool rather than stepping through the
      // other accounts of the current one, and opens there right away.
      if (key.name === "tab") {
        const selected = filtered[activeIndex]
        if (!selected) return
        const tabTarget = nextToolTarget(targets, selected.source, currentTarget())
        if (!tabTarget) return
        cleanup()
        resolve(finish(selected, tabTarget))
        return
      }

      if (key.ctrl && key.name === "t") {
        cycleTarget(1)
        render()
        return
      }

      if (key.name === "up") {
        activeIndex = clampIndex(activeIndex - 1, filtered.length)
        render()
        return
      }

      if (key.name === "down") {
        activeIndex = clampIndex(activeIndex + 1, filtered.length)
        render()
        return
      }

      if (key.name === "pageup") {
        activeIndex = clampIndex(activeIndex - 10, filtered.length)
        render()
        return
      }

      if (key.name === "pagedown") {
        activeIndex = clampIndex(activeIndex + 10, filtered.length)
        render()
        return
      }

      if (key.name === "home") {
        activeIndex = 0
        render()
        return
      }

      if (key.name === "end") {
        activeIndex = clampIndex(filtered.length - 1, filtered.length)
        render()
        return
      }

      if (key.name === "backspace") {
        query = query.slice(0, -1)
        activeIndex = 0
        render()
        return
      }

      const typed = queryInput(key)
      if (typed) {
        query += typed
        activeIndex = 0
        render()
      }
    }

    process.stdin.on("keypress", onKeypress)
    process.stdout.on("resize", render)
    render()
  })
}
