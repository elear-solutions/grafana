import {
  DataQueryResponse,
  DataFrame,
  isDataFrame,
  FieldType,
  QueryResultMeta,
  ArrayVector,
  Labels,
} from '@grafana/data';
import { LokiQuery, LokiQueryType } from './types';
import { makeTableFrames } from './makeTableFrames';
import { formatQuery, getHighlighterExpressionsFromQuery } from './query_utils';

function isMetricFrame(frame: DataFrame): boolean {
  return frame.fields.every((field) => field.type === FieldType.time || field.type === FieldType.number);
}

// returns a new frame, with meta merged with it's original meta
function setFrameMeta(frame: DataFrame, meta: QueryResultMeta): DataFrame {
  const { meta: oldMeta, ...rest } = frame;
  // meta maybe be undefined, we need to handle that
  const newMeta = { ...oldMeta, ...meta };
  return {
    ...rest,
    meta: newMeta,
  };
}

function decodeLabelsInJson(text: string): Labels {
  const array: Array<[string, string]> = JSON.parse(text);
  // NOTE: maybe we should go with maps, those have guaranteed ordering
  return Object.fromEntries(array);
}

function processStreamFrame(frame: DataFrame, query: LokiQuery | undefined): DataFrame {
  const meta: QueryResultMeta = {
    preferredVisualisationType: 'logs',
    searchWords: query !== undefined ? getHighlighterExpressionsFromQuery(formatQuery(query.expr)) : undefined,
    custom: {
      // used by logs_model
      lokiQueryStatKey: 'Summary: total bytes processed',
    },
  };
  const newFrame = setFrameMeta(frame, meta);

  const newFields = newFrame.fields.map((field) => {
    switch (field.name) {
      case 'labels': {
        // the labels, when coming from the server, are json-encoded.
        // here we decode them if needed.
        return field.config.custom.json
          ? {
              name: field.name,
              type: FieldType.other,
              config: field.config,
              // we are parsing the labels the same way as streaming-dataframes do
              values: new ArrayVector(field.values.toArray().map((text) => decodeLabelsInJson(text))),
            }
          : field;
      }
      case 'tsNs': {
        // we need to switch the field-type to be `time`
        return {
          ...field,
          type: FieldType.time,
        };
      }
      default: {
        // no modification needed
        return field;
      }
    }
  });

  return {
    ...newFrame,
    fields: newFields,
  };
}

function processStreamsFrames(frames: DataFrame[], queryMap: Map<string, LokiQuery>): DataFrame[] {
  return frames.map((frame) => {
    const query = frame.refId !== undefined ? queryMap.get(frame.refId) : undefined;
    return processStreamFrame(frame, query);
  });
}

function processMetricInstantFrames(frames: DataFrame[]): DataFrame[] {
  return frames.length > 0 ? makeTableFrames(frames) : [];
}

function processMetricRangeFrames(frames: DataFrame[]): DataFrame[] {
  const meta: QueryResultMeta = { preferredVisualisationType: 'graph' };
  return frames.map((frame) => setFrameMeta(frame, meta));
}

// we split the frames into 3 groups, because we will handle
// each group slightly differently
function groupFrames(
  frames: DataFrame[],
  queryMap: Map<string, LokiQuery>
): {
  streamsFrames: DataFrame[];
  metricInstantFrames: DataFrame[];
  metricRangeFrames: DataFrame[];
} {
  const streamsFrames: DataFrame[] = [];
  const metricInstantFrames: DataFrame[] = [];
  const metricRangeFrames: DataFrame[] = [];

  frames.forEach((frame) => {
    if (!isMetricFrame(frame)) {
      streamsFrames.push(frame);
    } else {
      const isInstantFrame = frame.refId != null && queryMap.get(frame.refId)?.queryType === LokiQueryType.Instant;
      if (isInstantFrame) {
        metricInstantFrames.push(frame);
      } else {
        metricRangeFrames.push(frame);
      }
    }
  });

  return { streamsFrames, metricInstantFrames, metricRangeFrames };
}

export function transformBackendResult(response: DataQueryResponse, queries: LokiQuery[]): DataQueryResponse {
  const { data, ...rest } = response;

  // in the typescript type, data is an array of basically anything.
  // we do know that they have to be dataframes, so we make a quick check,
  // this way we can be sure, and also typescript is happy.
  const dataFrames = data.map((d) => {
    if (!isDataFrame(d)) {
      throw new Error('transformation only supports dataframe responses');
    }
    return d;
  });

  const queryMap = new Map(queries.map((query) => [query.refId, query]));

  const { streamsFrames, metricInstantFrames, metricRangeFrames } = groupFrames(dataFrames, queryMap);

  return {
    ...rest,
    data: [
      ...processMetricRangeFrames(metricRangeFrames),
      ...processMetricInstantFrames(metricInstantFrames),
      ...processStreamsFrames(streamsFrames, queryMap),
    ],
  };
}
