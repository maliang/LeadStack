import { t } from '../i18n'
/** 极简 cron 解析与人话预览（5 字段：分 时 日 月 周）。返回 null 表示格式非法。 */

export interface CronField {
  raw: string
  values: Set<number>
  wildcard: boolean
}

const RANGES: [number, number][] = [
  [0, 59],
  [0, 23],
  [1, 31],
  [1, 12],
  [0, 6],
]

function parseField(raw: string, min: number, max: number): CronField | null {
  const field = raw.trim()
  if (field === '') return null
  const values = new Set<number>()
  const parts = field.split(',')
  for (const part of parts) {
    if (part === '*') {
      for (let value = min; value <= max; value += 1) values.add(value)
      continue
    }
    const stepMatch = /^\*\/(\d+)$/.exec(part)
    if (stepMatch) {
      const step = Number(stepMatch[1])
      if (!Number.isInteger(step) || step <= 0) return null
      for (let value = min; value <= max; value += step) values.add(value)
      continue
    }
    const rangeStep = /^(\d+)-(\d+)\/(\d+)$/.exec(part)
    if (rangeStep) {
      const start = Number(rangeStep[1])
      const end = Number(rangeStep[2])
      const step = Number(rangeStep[3])
      if (start < min || end > max || start > end || step <= 0) return null
      for (let value = start; value <= end; value += step) values.add(value)
      continue
    }
    const range = /^(\d+)-(\d+)$/.exec(part)
    if (range) {
      const start = Number(range[1])
      const end = Number(range[2])
      if (start < min || end > max || start > end) return null
      for (let value = start; value <= end; value += 1) values.add(value)
      continue
    }
    const single = /^(\d+)$/.exec(part)
    if (single) {
      const value = Number(single[1])
      if (value < min || value > max) return null
      values.add(value)
      continue
    }
    return null
  }
  return { raw: field, values, wildcard: field === '*' }
}

export interface ParsedCron {
  minute: CronField
  hour: CronField
  day: CronField
  month: CronField
  weekday: CronField
}

export function parseCron(expression: string): ParsedCron | null {
  const parts = expression.trim().split(/\s+/)
  if (parts.length !== 5) return null
  const fields: CronField[] = []
  for (let index = 0; index < 5; index += 1) {
    const [min, max] = RANGES[index]
    const parsed = parseField(parts[index], min, max)
    if (!parsed) return null
    fields.push(parsed)
  }
  return {
    minute: fields[0],
    hour: fields[1],
    day: fields[2],
    month: fields[3],
    weekday: fields[4],
  }
}

function list(values: Set<number>, formatter?: (value: number) => string): string {
  const sorted = [...values].sort((a, b) => a - b)
  return sorted.map((value) => (formatter ? formatter(value) : String(value))).join(', ')
}

const WEEKDAY_NAMES: Record<number, string> = {
  0: t('s.0612'),
  1: t('s.0613'),
  2: t('s.0614'),
  3: t('s.0615'),
  4: t('s.0616'),
  5: t('s.0617'),
  6: t('s.0618'),
}

/** 生成人话预览：例如「每天 03:00」「每周一 09:30」「每小时的第 5 分钟」。 */
export function describeCron(expression: string): string | null {
  const parsed = parseCron(expression)
  if (!parsed) return null
  const { minute, hour, day, month, weekday } = parsed

  const time = `${list(hour.values, (value) => String(value).padStart(2, '0'))}:${list(
    minute.values,
    (value) => String(value).padStart(2, '0'),
  )}`

  if (minute.wildcard && hour.wildcard && day.wildcard && month.wildcard && weekday.wildcard) {
    return t('s.0619')
  }
  if (hour.wildcard && day.wildcard && month.wildcard && weekday.wildcard) {
    return t('s.0620', { '0': list(minute.values) })
  }
  if (day.wildcard && month.wildcard && weekday.wildcard) {
    return t('s.0621', { '0': time })
  }
  if (day.wildcard && month.wildcard && !weekday.wildcard) {
    return t('s.0622', { '0': list(weekday.values, (value) => WEEKDAY_NAMES[value] ?? String(value)), '1': time })
  }
  if (month.wildcard && weekday.wildcard) {
    return t('s.0623', { '0': list(day.values), '1': time })
  }
  return t('s.0624', { '0': expression.trim(), '1': time, '2': list(month.values), '3': list(day.values), '4': list(weekday.values) })
}
