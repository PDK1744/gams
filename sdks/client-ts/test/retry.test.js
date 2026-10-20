import test from 'node:test';
import assert from 'node:assert/strict';
import { withConflictRetry, calculateBackoff, ConflictError, ValidationError } from '../dist/index.js';

test('calculateBackoff calculates exponential delay with bounds', () => {
  const d0 = calculateBackoff(0, 100, 1000);
  assert.ok(d0 >= 100 && d0 <= 200, `d0=${d0}`);

  const d1 = calculateBackoff(1, 100, 1000);
  assert.ok(d1 >= 200 && d1 <= 350, `d1=${d1}`);

  const dMax = calculateBackoff(10, 100, 500);
  assert.equal(dMax, 500);
});

test('withConflictRetry succeeds on first attempt if no conflict', async () => {
  let attempts = 0;
  const result = await withConflictRetry(async () => {
    attempts++;
    return 'success';
  });
  assert.equal(result, 'success');
  assert.equal(attempts, 1);
});

test('withConflictRetry retries on ConflictError and recovers', async () => {
  let attempts = 0;
  const result = await withConflictRetry(
    async (att) => {
      attempts++;
      if (att < 2) {
        throw new ConflictError('Version conflict', { conflict: { current_version: att + 1 } });
      }
      return 'recovered';
    },
    { maxAttempts: 3, baseDelayMs: 10, maxDelayMs: 50 }
  );

  assert.equal(result, 'recovered');
  assert.equal(attempts, 3);
});

test('withConflictRetry does not retry on non-conflict errors', async () => {
  let attempts = 0;
  await assert.rejects(
    async () => {
      await withConflictRetry(
        async () => {
          attempts++;
          throw new ValidationError('Bad request');
        },
        { maxAttempts: 3, baseDelayMs: 10 }
      );
    },
    (err) => {
      assert.ok(err instanceof ValidationError);
      assert.equal(attempts, 1);
      return true;
    }
  );
});

test('withConflictRetry throws after exhausting max attempts', async () => {
  let attempts = 0;
  await assert.rejects(
    async () => {
      await withConflictRetry(
        async () => {
          attempts++;
          throw new ConflictError('Always conflict');
        },
        { maxAttempts: 3, baseDelayMs: 10, maxDelayMs: 20 }
      );
    },
    (err) => {
      assert.ok(err instanceof ConflictError);
      assert.equal(attempts, 3);
      return true;
    }
  );
});
