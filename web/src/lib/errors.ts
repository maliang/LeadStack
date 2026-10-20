import { t } from '../i18n'
export function errorMessage(error: unknown, fallback: string = t('s.0625')): string {
  if (error instanceof Error && error.message.trim() !== '') return error.message
  return fallback
}
