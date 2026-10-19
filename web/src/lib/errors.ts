export function errorMessage(error: unknown, fallback: string = '操作失败，请稍后重试'): string {
  if (error instanceof Error && error.message.trim() !== '') return error.message
  return fallback
}
