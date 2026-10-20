import {
  GAMSClientError,
  AuthenticationError,
  ForbiddenError,
  NotFoundError,
  ConflictError,
  ValidationError,
} from './errors.js';
import { withConflictRetry } from './retry.js';
import type {
  GAMSClientOptions,
  RememberInput,
  RememberResult,
  RecallInput,
  RecallResult,
  ForgetResult,
  InspectResult,
  Namespace,
  EnsureNamespaceOptions,
} from './types.js';

export class GAMSClient {
  public readonly baseUrl: string;
  public readonly apiKey: string;
  public readonly timeoutMs: number;
  public readonly retryPolicy?: GAMSClientOptions['retryPolicy'];

  constructor(options: GAMSClientOptions) {
    if (!options?.apiKey) {
      throw new ValidationError('GAMSClient requires a non-empty apiKey');
    }
    this.baseUrl = (options.baseUrl ?? 'http://127.0.0.1:8080').replace(/\/+$/, '');
    this.apiKey = options.apiKey;
    this.timeoutMs = options.timeoutMs ?? 30_000;
    this.retryPolicy = options.retryPolicy;
  }

  private async request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const url = `${this.baseUrl}${path.startsWith('/') ? path : `/${path}`}`;
    const headers = new Headers(options.headers);
    headers.set('Authorization', `Bearer ${this.apiKey}`);
    if (!headers.has('Content-Type') && options.body) {
      headers.set('Content-Type', 'application/json');
    }

    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeoutMs);

    let res: Response;
    try {
      res = await fetch(url, {
        ...options,
        headers,
        signal: controller.signal,
      });
    } catch (err: any) {
      if (err.name === 'AbortError') {
        throw new GAMSClientError(`Request timeout after ${this.timeoutMs}ms`);
      }
      throw new GAMSClientError(`Network error: ${err.message}`);
    } finally {
      clearTimeout(timeoutId);
    }

    let bodyData: any = null;
    const contentType = res.headers.get('content-type') || '';
    if (contentType.includes('application/json')) {
      try {
        bodyData = await res.json();
      } catch {
        // empty or unparseable JSON
      }
    } else {
      bodyData = await res.text();
    }

    if (!res.ok) {
      const message =
        (typeof bodyData === 'object' && bodyData?.error) ||
        (typeof bodyData === 'string' && bodyData) ||
        `HTTP ${res.status}: ${res.statusText}`;

      switch (res.status) {
        case 400:
          throw new ValidationError(message, bodyData);
        case 401:
          throw new AuthenticationError(message, bodyData);
        case 403:
          throw new ForbiddenError(message, bodyData);
        case 404:
          throw new NotFoundError(message, bodyData);
        case 409:
          throw new ConflictError(message, bodyData);
        default:
          throw new GAMSClientError(message, res.status, bodyData);
      }
    }

    return bodyData as T;
  }

  /**
   * Store or update a claim in memory.
   * If input.base_version is specified and a 409 conflict occurs, automatically
   * retries with exponential backoff if retryOnConflict is enabled.
   */
  public async remember(
    input: RememberInput,
    retryOnConflict: boolean = true
  ): Promise<RememberResult> {
    const execute = async (attempt: number): Promise<RememberResult> => {
      // If retrying due to conflict and we have a claim_id, fetch latest version
      if (attempt > 0 && input.claim_id) {
        try {
          const inspected = await this.inspect(input.claim_id);
          input.base_version = inspected.claim.current_version;
        } catch {
          // If inspect fails, continue with original input
        }
      }

      return this.request<RememberResult>('/v1/claims', {
        method: 'POST',
        body: JSON.stringify(input),
      });
    };

    if (retryOnConflict && input.claim_id) {
      return withConflictRetry(execute, this.retryPolicy);
    }

    return execute(0);
  }

  /**
   * Recall the most relevant claims for a query using hybrid text relevance,
   * confidence, and recency scoring.
   */
  public async recall(input: RecallInput): Promise<RecallResult> {
    return this.request<RecallResult>('/v1/recall', {
      method: 'POST',
      body: JSON.stringify(input),
    });
  }

  /**
   * Mark a claim as forgotten (soft delete).
   */
  public async forget(claimId: string, reason: string): Promise<ForgetResult> {
    if (!claimId) {
      throw new ValidationError('claimId is required');
    }
    if (!reason) {
      throw new ValidationError('reason is required');
    }
    return this.request<ForgetResult>(`/v1/claims/${encodeURIComponent(claimId)}/forget`, {
      method: 'POST',
      body: JSON.stringify({ reason }),
    });
  }

  /**
   * Retrieve full governance history, versions, evidence, and audit trail for a claim.
   */
  public async inspect(claimId: string): Promise<InspectResult> {
    if (!claimId) {
      throw new ValidationError('claimId is required');
    }
    return this.request<InspectResult>(`/v1/claims/${encodeURIComponent(claimId)}`, {
      method: 'GET',
    });
  }

  /**
   * List namespaces accessible to the calling API key.
   */
  public async listNamespaces(): Promise<Namespace[]> {
    return this.request<Namespace[]>('/v1/namespaces', {
      method: 'GET',
    });
  }

  /**
   * Idempotent helper: returns namespace if it exists, otherwise creates it.
   */
  public async ensureNamespace(
    slug: string,
    opts?: EnsureNamespaceOptions
  ): Promise<Namespace> {
    if (!slug) {
      throw new ValidationError('slug is required');
    }

    // 1. Try to get existing namespace
    try {
      return await this.request<Namespace>(`/v1/namespaces/${encodeURIComponent(slug)}`, {
        method: 'GET',
      });
    } catch (err) {
      if (!(err instanceof NotFoundError) && (err as any)?.statusCode !== 404) {
        throw err;
      }
    }

    // 2. Namespace does not exist; create it
    try {
      return await this.request<Namespace>('/v1/namespaces', {
        method: 'POST',
        body: JSON.stringify({
          slug,
          name: opts?.name || slug,
          description: opts?.description,
          access_mode: opts?.accessMode || 'tenant_wide',
        }),
      });
    } catch (err) {
      // If another process created it concurrently, fetch and return
      if (err instanceof ConflictError || (err as any)?.statusCode === 409) {
        return this.request<Namespace>(`/v1/namespaces/${encodeURIComponent(slug)}`, {
          method: 'GET',
        });
      }
      throw err;
    }
  }
}

export default GAMSClient;
