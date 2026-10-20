import { ConflictError } from './errors.js';
import { RetryPolicy } from './types.js';

const DEFAULT_MAX_ATTEMPTS = 3;
const DEFAULT_BASE_DELAY_MS = 100;
const DEFAULT_MAX_DELAY_MS = 2000;

export async function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export function calculateBackoff(attempt: number, baseDelay: number, maxDelay: number): number {
  const exp = Math.min(attempt, 6);
  const factor = Math.pow(2, exp);
  const delay = baseDelay * factor;
  // Add 10-25% random jitter to avoid thundering herds
  const jitter = delay * (0.1 + Math.random() * 0.15);
  return Math.min(delay + jitter, maxDelay);
}

export async function withConflictRetry<T>(
  operation: (attempt: number) => Promise<T>,
  policy?: RetryPolicy
): Promise<T> {
  const maxAttempts = policy?.maxAttempts ?? DEFAULT_MAX_ATTEMPTS;
  const baseDelayMs = policy?.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
  const maxDelayMs = policy?.maxDelayMs ?? DEFAULT_MAX_DELAY_MS;

  let lastError: unknown;

  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    try {
      return await operation(attempt);
    } catch (err) {
      lastError = err;
      const isConflict =
        err instanceof ConflictError ||
        (err as any)?.statusCode === 409;

      if (!isConflict || attempt === maxAttempts - 1) {
        throw err;
      }

      const delay = calculateBackoff(attempt, baseDelayMs, maxDelayMs);
      await sleep(delay);
    }
  }

  throw lastError;
}
