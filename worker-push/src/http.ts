export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

// Forge in a browser calls the relay from another origin. Any origin may: a
// request is authorized by its signature, and the relay sets no cookie.
const headers = {
  "content-type": "application/json; charset=utf-8",
  "cache-control": "no-store",
  "x-content-type-options": "nosniff",
  "access-control-allow-origin": "*",
};

export function preflightResponse(): Response {
  return new Response(null, {
    status: 204,
    headers: {
      "access-control-allow-origin": "*",
      "access-control-allow-methods": "GET, POST, PUT, DELETE",
      "access-control-allow-headers":
        "content-type, x-forge-principal-type, x-forge-principal-id, x-forge-timestamp, x-forge-nonce, x-forge-signature",
      "access-control-max-age": "86400",
    },
  });
}

export function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers });
}

export function errorResponse(error: unknown): Response {
  if (error instanceof ApiError) {
    return json({ error: { code: error.code, message: error.message } }, error.status);
  }
  console.error("Unhandled push relay error", error);
  return json(
    { error: { code: "internal_error", message: "The push relay could not process the request" } },
    500,
  );
}

export async function readJson<T>(request: Request, maxBytes = 16 * 1024): Promise<{ body: T; bytes: Uint8Array }> {
  if (request.headers.get("content-type")?.split(";", 1)[0]?.trim() !== "application/json") {
    throw new ApiError(415, "unsupported_media_type", "Content-Type must be application/json");
  }
  const contentLength = Number(request.headers.get("content-length") ?? "0");
  if (Number.isFinite(contentLength) && contentLength > maxBytes) {
    throw new ApiError(413, "body_too_large", "Request body is too large");
  }
  const bytes = new Uint8Array(await request.arrayBuffer());
  if (bytes.length > maxBytes) throw new ApiError(413, "body_too_large", "Request body is too large");
  try {
    return { body: JSON.parse(new TextDecoder().decode(bytes)) as T, bytes };
  } catch {
    throw new ApiError(400, "invalid_json", "Request body must be valid JSON");
  }
}

export function requireText(value: unknown, field: string, maximum = 256): string {
  if (typeof value !== "string" || value.length === 0 || value.length > maximum) {
    throw new ApiError(400, "invalid_request", `${field} must be a non-empty string of at most ${maximum} characters`);
  }
  return value;
}

export function optionalText(value: unknown, field: string, maximum = 100): string | null {
  if (value === undefined || value === null || value === "") return null;
  if (typeof value !== "string" || value.length > maximum) {
    throw new ApiError(400, "invalid_request", `${field} must be a string of at most ${maximum} characters`);
  }
  return value;
}
