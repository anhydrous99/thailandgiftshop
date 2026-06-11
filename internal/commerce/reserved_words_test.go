package commerce

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// dynamoReservedWords is the complete reserved-words list from
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ReservedWords.html.
// Any of these used as a bare attribute name in an expression makes the real
// DynamoDB service reject the request with a ValidationException, which fake
// clients never catch.
const dynamoReservedWords = `
ABORT ABSOLUTE ACTION ADD AFTER AGENT AGGREGATE ALL ALLOCATE ALTER ANALYZE AND
ANY ARCHIVE ARE ARRAY AS ASC ASCII ASENSITIVE ASSERTION ASYMMETRIC AT ATOMIC
ATTACH ATTRIBUTE AUTH AUTHORIZATION AUTHORIZE AUTO AVG BACK BACKUP BASE BATCH
BEFORE BEGIN BETWEEN BIGINT BINARY BIT BLOB BLOCK BOOLEAN BOTH BREADTH BUCKET
BULK BY BYTE CALL CALLED CALLING CAPACITY CASCADE CASCADED CASE CAST CATALOG
CHAR CHARACTER CHECK CLASS CLOB CLOSE CLUSTER CLUSTERED CLUSTERING CLUSTERS
COALESCE COLLATE COLLATION COLLECTION COLUMN COLUMNS COMBINE COMMENT COMMIT
COMPACT COMPILE COMPRESS CONDITION CONFLICT CONNECT CONNECTION CONSISTENCY
CONSISTENT CONSTRAINT CONSTRAINTS CONSTRUCTOR CONSUMED CONTINUE CONVERT COPY
CORRESPONDING COUNT COUNTER CREATE CROSS CUBE CURRENT CURSOR CYCLE DATA
DATABASE DATE DATETIME DAY DEALLOCATE DEC DECIMAL DECLARE DEFAULT DEFERRABLE
DEFERRED DEFINE DEFINED DEFINITION DELETE DELIMITED DEPTH DEREF DESC DESCRIBE
DESCRIPTOR DETACH DETERMINISTIC DIAGNOSTICS DIRECTORIES DISABLE DISCONNECT
DISTINCT DISTRIBUTE DO DOMAIN DOUBLE DROP DUMP DURATION DYNAMIC EACH ELEMENT
ELSE ELSEIF EMPTY ENABLE END EQUAL EQUALS ERROR ESCAPE ESCAPED EVAL EVALUATE
EXCEEDED EXCEPT EXCEPTION EXCEPTIONS EXCLUSIVE EXEC EXECUTE EXISTS EXIT
EXPLAIN EXPLODE EXPORT EXPRESSION EXTENDED EXTERNAL EXTRACT FAIL FALSE FAMILY
FETCH FIELDS FILE FILTER FILTERING FINAL FINISH FIRST FIXED FLATTERN FLOAT FOR
FORCE FOREIGN FORMAT FORWARD FOUND FREE FROM FULL FUNCTION FUNCTIONS GENERAL
GENERATE GET GLOB GLOBAL GO GOTO GRANT GREATER GROUP GROUPING HANDLER HASH
HAVE HAVING HEAP HIDDEN HOLD HOUR IDENTIFIED IDENTITY IF IGNORE IMMEDIATE
IMPORT IN INCLUDING INCLUSIVE INCREMENT INCREMENTAL INDEX INDEXED INDEXES
INDICATOR INFINITE INITIALLY INLINE INNER INNTER INOUT INPUT INSENSITIVE
INSERT INSTEAD INT INTEGER INTERSECT INTERVAL INTO INVALIDATE IS ISOLATION
ITEM ITEMS ITERATE JOIN KEY KEYS LAG LANGUAGE LARGE LAST LATERAL LEAD LEADING
LEAVE LEFT LENGTH LESS LEVEL LIKE LIMIT LIMITED LINES LIST LOAD LOCAL
LOCALTIME LOCALTIMESTAMP LOCATION LOCATOR LOCK LOCKS LOG LOGED LONG LOOP LOWER
MAP MATCH MATERIALIZED MAX MAXLEN MEMBER MERGE METHOD METRICS MIN MINUS MINUTE
MISSING MOD MODE MODIFIES MODIFY MODULE MONTH MULTI MULTISET NAME NAMES
NATIONAL NATURAL NCHAR NCLOB NEW NEXT NO NONE NOT NULL NULLIF NUMBER NUMERIC
OBJECT OF OFFLINE OFFSET OLD ON ONLINE ONLY OPAQUE OPEN OPERATOR OPTION OR
ORDER ORDINALITY OTHER OTHERS OUT OUTER OUTPUT OVER OVERLAPS OVERRIDE OWNER
PAD PARALLEL PARAMETER PARAMETERS PARTIAL PARTITION PARTITIONED PARTITIONS
PATH PERCENT PERCENTILE PERMISSION PERMISSIONS PIPE PIPELINED PLAN POOL
POSITION PRECISION PREPARE PRESERVE PRIMARY PRIOR PRIVATE PRIVILEGES PROCEDURE
PROCESSED PROJECT PROJECTION PROPERTY PROVISIONING PUBLIC PUT QUERY QUIT
QUORUM RAISE RANDOM RANGE RANK RAW READ READS REAL REBUILD RECORD RECURSIVE
REDUCE REF REFERENCE REFERENCES REFERENCING REGEXP REGION REINDEX RELATIVE
RELEASE REMAINDER RENAME REPEAT REPLACE REQUEST RESET RESIGNAL RESOURCE
RESPONSE RESTORE RESTRICT RESULT RETURN RETURNING RETURNS REVERSE REVOKE RIGHT
ROLE ROLES ROLLBACK ROLLUP ROUTINE ROW ROWS RULE RULES SAMPLE SATISFIES SAVE
SAVEPOINT SCAN SCHEMA SCOPE SCROLL SEARCH SECOND SECTION SEGMENT SEGMENTS
SELECT SELF SEMI SENSITIVE SEPARATE SEQUENCE SERIALIZABLE SESSION SET SETS
SHARD SHARE SHARED SHORT SHOW SIGNAL SIMILAR SIZE SKEWED SMALLINT SNAPSHOT
SOME SOURCE SPACE SPACES SPARSE SPECIFIC SPECIFICTYPE SPLIT SQL SQLCODE
SQLERROR SQLEXCEPTION SQLSTATE SQLWARNING START STATE STATIC STATUS STORAGE
STORE STORED STREAM STRING STRUCT STYLE SUB SUBMULTISET SUBPARTITION SUBSTRING
SUBTYPE SUM SUPER SYMMETRIC SYNONYM SYSTEM TABLE TABLESAMPLE TEMP TEMPORARY
TERMINATED TEXT THAN THEN THROUGHPUT TIME TIMESTAMP TIMEZONE TINYINT TO TOKEN
TOTAL TOUCH TRAILING TRANSACTION TRANSFORM TRANSLATE TRANSLATION TREAT TRIGGER
TRIM TRUE TRUNCATE TTL TUPLE TYPE UNDER UNDO UNION UNIQUE UNIT UNKNOWN
UNLOGGED UNNEST UNPROCESSED UNSIGNED UNTIL UPDATE UPPER URL USAGE USE USER
USERS USING UUID VACUUM VALUE VALUED VALUES VARCHAR VARIABLE VARIANCE VARINT
VARYING VIEW VIEWS VIRTUAL VOID WAIT WHEN WHENEVER WHERE WHILE WINDOW WITH
WITHIN WITHOUT WORK WRAPPED WRITE YEAR ZONE
`

// expressionSyntaxTokens are expression keywords and functions — legitimate
// non-attribute tokens inside condition/update/key-condition expressions.
var expressionSyntaxTokens = map[string]bool{
	"SET": true, "REMOVE": true, "ADD": true, "DELETE": true,
	"AND": true, "OR": true, "NOT": true, "IN": true, "BETWEEN": true,
	"ATTRIBUTE_EXISTS": true, "ATTRIBUTE_NOT_EXISTS": true, "ATTRIBUTE_TYPE": true,
	"BEGINS_WITH": true, "CONTAINS": true, "SIZE": true,
	"LIST_APPEND": true, "IF_NOT_EXISTS": true,
}

var expressionIdentifierPattern = regexp.MustCompile(`[#:]?[A-Za-z][A-Za-z0-9_]*`)

// bareExpressionAttributeNames extracts the tokens of an expression that act
// as literal attribute names: identifiers that are neither expression
// attribute names (#...), value placeholders (:...), nor expression syntax.
func bareExpressionAttributeNames(expression string) []string {
	var names []string
	for _, token := range expressionIdentifierPattern.FindAllString(expression, -1) {
		if strings.HasPrefix(token, "#") || strings.HasPrefix(token, ":") {
			continue
		}
		if expressionSyntaxTokens[strings.ToUpper(token)] {
			continue
		}
		names = append(names, token)
	}
	return names
}

// TestWriteExpressionsContainNoUnaliasedReservedWords drives every store
// method that emits an expression string and asserts no DynamoDB reserved
// word appears as a bare attribute name — those fail with a
// ValidationException on the real service even though fake clients accept
// them (the bug this guards against: UpdateAddress's un-aliased "region").
func TestWriteExpressionsContainNoUnaliasedReservedWords(t *testing.T) {
	reserved := map[string]bool{}
	for _, word := range strings.Fields(dynamoReservedWords) {
		reserved[word] = true
	}
	if len(reserved) < 500 {
		t.Fatalf("reserved-words list looks truncated: %d entries", len(reserved))
	}

	client := newFakeCommerceClient()
	clock := newTestClock()
	store := NewDynamoStoreWithClock(client, testDynamoConfig(), nil, clock.Now)
	ctx := context.Background()

	var customer Customer
	var address Address
	var order Order

	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"CreateCustomer", func(t *testing.T) {
			var err error
			customer, err = store.CreateCustomer(ctx, "Shopper@example.test", "shopper@example.test", testPasswordHash)
			if err != nil {
				t.Fatalf("CreateCustomer returned error: %v", err)
			}
		}},
		{"SetStripeCustomerID", func(t *testing.T) {
			if _, err := store.SetStripeCustomerID(ctx, customer.ID, "cus_123"); err != nil {
				t.Fatalf("SetStripeCustomerID returned error: %v", err)
			}
		}},
		{"SetDefaultAddress set", func(t *testing.T) {
			if err := store.SetDefaultAddress(ctx, customer.ID, "addr1", 1); err != nil {
				t.Fatalf("SetDefaultAddress returned error: %v", err)
			}
		}},
		{"SetDefaultAddress clear", func(t *testing.T) {
			if err := store.SetDefaultAddress(ctx, customer.ID, "", 2); err != nil {
				t.Fatalf("SetDefaultAddress clear returned error: %v", err)
			}
		}},
		{"UpdatePassword", func(t *testing.T) {
			if err := store.UpdatePassword(ctx, customer.ID, testPasswordHash, 3); err != nil {
				t.Fatalf("UpdatePassword returned error: %v", err)
			}
		}},
		{"PutCart", func(t *testing.T) {
			if _, err := store.PutCart(ctx, CartRecord{CustomerID: customer.ID}); err != nil {
				t.Fatalf("PutCart returned error: %v", err)
			}
		}},
		{"CreateAddress", func(t *testing.T) {
			var err error
			address, err = store.CreateAddress(ctx, Address{
				CustomerID: customer.ID,
				FullName:   "A Shopper",
				Line1:      "1 Sukhumvit Road",
				City:       "Bangkok",
				Region:     "Bangkok",
				PostalCode: "10110",
				Country:    "TH",
			})
			if err != nil {
				t.Fatalf("CreateAddress returned error: %v", err)
			}
		}},
		{"UpdateAddress", func(t *testing.T) {
			updated := address
			updated.Region = "Chiang Mai"
			updated.Line2 = ""
			updated.Phone = ""
			if _, err := store.UpdateAddress(ctx, updated, address.Version); err != nil {
				t.Fatalf("UpdateAddress returned error: %v", err)
			}
		}},
		{"CreateOrder", func(t *testing.T) {
			var err error
			order, err = store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}
		}},
		{"PatchOrder", func(t *testing.T) {
			now := clock.Now()
			if _, err := store.PatchOrder(ctx, order.ID, OrderStatusPendingPayment, order.Version, OrderPatch{
				StripeCheckoutSessionID: ptr("cs_123"),
				StripePaymentIntentID:   ptr(""),
				PaymentCardBrand:        ptr("visa"),
				PaymentCardLast4:        ptr("4242"),
				TrackingCarrier:         ptr(""),
				TrackingNumber:          ptr(""),
				CheckoutAttempt:         ptr(1),
				PaidAt:                  &time.Time{},
				ShippedAt:               &now,
				DeliveredAt:             &time.Time{},
				StockReleasedAt:         &now,
			}); err != nil {
				t.Fatalf("PatchOrder returned error: %v", err)
			}
		}},
		{"TransitionOrder", func(t *testing.T) {
			now := clock.Now()
			if _, err := store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusCanceled, OrderPatch{
				Actor:           OrderActorSystem,
				StockReleasedAt: &now,
			}); err != nil {
				t.Fatalf("TransitionOrder returned error: %v", err)
			}
		}},
		{"MarkStripeEventProcessed", func(t *testing.T) {
			if _, err := store.MarkStripeEventProcessed(ctx, "evt_audit", "checkout.session.expired", order.ID); err != nil {
				t.Fatalf("MarkStripeEventProcessed returned error: %v", err)
			}
		}},
		{"ReserveEmailEvent", func(t *testing.T) {
			reserved, err := store.ReserveEmailEvent(ctx, EmailEvent{Key: "order-placed-v1", Kind: "order_placed", OrderID: order.ID, OrderVersion: order.Version, To: "shopper@example.test", Status: "reserved", Attempts: 1, CreatedAt: clock.Now()})
			if err != nil || !reserved {
				t.Fatalf("ReserveEmailEvent reserved=%v err=%v, want reserved", reserved, err)
			}
		}},
		{"MarkEmailEventSent", func(t *testing.T) {
			if err := store.MarkEmailEventSent(ctx, order.ID, "order-placed-v1", clock.Now()); err != nil {
				t.Fatalf("MarkEmailEventSent returned error: %v", err)
			}
		}},
		{"MarkEmailEventFailed", func(t *testing.T) {
			reserved, err := store.ReserveEmailEvent(ctx, EmailEvent{Key: "status-paid-v1", Kind: "status_change", OrderID: order.ID, OrderVersion: order.Version, To: "shopper@example.test", Status: "reserved", Attempts: 1, CreatedAt: clock.Now()})
			if err != nil || !reserved {
				t.Fatalf("ReserveEmailEvent for failed marker reserved=%v err=%v, want reserved", reserved, err)
			}
			if err := store.MarkEmailEventFailed(ctx, order.ID, "status-paid-v1", clock.Now(), "provider rejected recipient"); err != nil {
				t.Fatalf("MarkEmailEventFailed returned error: %v", err)
			}
		}},
		{"ReserveLoginAttempt", func(t *testing.T) {
			// Walk the new-window, increment, and locking expressions.
			for attempt := 0; attempt < LoginAttemptLimit; attempt++ {
				if _, err := store.ReserveLoginAttempt(ctx, "IP#hashed", clock.Now()); err != nil {
					t.Fatalf("ReserveLoginAttempt returned error: %v", err)
				}
			}
		}},
		{"DeleteAllSessions", func(t *testing.T) {
			session := Session{CustomerID: customer.ID, TokenHash: "tokenhash", Nonce: "nonce", CreatedAt: clock.Now(), ExpiresAt: clock.Now().Add(time.Hour)}
			if err := store.PutSession(ctx, session); err != nil {
				t.Fatalf("PutSession returned error: %v", err)
			}
			if err := store.DeleteAllSessions(ctx, customer.ID); err != nil {
				t.Fatalf("DeleteAllSessions returned error: %v", err)
			}
		}},
		{"ConsumePasswordResetToken", func(t *testing.T) {
			token := PasswordResetToken{CustomerID: customer.ID, TokenHash: "reset-token-hash", CreatedAt: clock.Now(), ExpiresAt: clock.Now().Add(time.Hour), Version: 1}
			if err := store.PutPasswordResetToken(ctx, token); err != nil {
				t.Fatalf("PutPasswordResetToken returned error: %v", err)
			}
			if _, found, err := store.ConsumePasswordResetToken(ctx, customer.ID, "reset-token-hash", clock.Now()); err != nil || !found {
				t.Fatalf("ConsumePasswordResetToken found=%v err=%v, want found", found, err)
			}
		}},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			client.drainExpressions()
			step.run(t)
			expressions := client.drainExpressions()
			if len(expressions) == 0 {
				t.Fatalf("%s captured no expressions; the audit is not exercising it", step.name)
			}
			for _, expression := range expressions {
				for _, attribute := range bareExpressionAttributeNames(expression) {
					if reserved[strings.ToUpper(attribute)] {
						t.Errorf("expression %q uses DynamoDB reserved word %q as a bare attribute name; alias it via ExpressionAttributeNames", expression, attribute)
					}
				}
			}
		})
	}
}
