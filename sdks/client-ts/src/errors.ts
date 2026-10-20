/**
 * Custom error hierarchy for @gams/client.
 */

export class GAMSClientError extends Error {
  public readonly statusCode?: number;
  public readonly data?: any;

  constructor(message: string, statusCode?: number, data?: any) {
    super(message);
    this.name = 'GAMSClientError';
    this.statusCode = statusCode;
    this.data = data;
    Object.setPrototypeOf(this, new.target.prototype);
  }
}

export class AuthenticationError extends GAMSClientError {
  constructor(message: string = 'Invalid or missing API key', data?: any) {
    super(message, 401, data);
    this.name = 'AuthenticationError';
  }
}

export class ForbiddenError extends GAMSClientError {
  constructor(message: string = 'Operation forbidden', data?: any) {
    super(message, 403, data);
    this.name = 'ForbiddenError';
  }
}

export class NotFoundError extends GAMSClientError {
  constructor(message: string = 'Resource not found', data?: any) {
    super(message, 404, data);
    this.name = 'NotFoundError';
  }
}

export class ConflictError extends GAMSClientError {
  public readonly currentVersion?: number;
  public readonly updatedAt?: string;

  constructor(message: string = 'Optimistic concurrency conflict', data?: any) {
    super(message, 409, data);
    this.name = 'ConflictError';
    if (data?.conflict) {
      this.currentVersion = data.conflict.current_version;
      this.updatedAt = data.conflict.updated_at;
    }
  }
}

export class ValidationError extends GAMSClientError {
  constructor(message: string = 'Invalid request parameters', data?: any) {
    super(message, 400, data);
    this.name = 'ValidationError';
  }
}
